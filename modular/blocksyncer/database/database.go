package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/forbole/juno/v4/database"
	"github.com/forbole/juno/v4/database/mysql"
	"github.com/forbole/juno/v4/database/sqlclient"
	"github.com/forbole/juno/v4/log"
	driver_mysql "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"

	"github.com/mocachain/moca-storage-provider/store/bsdb"
)

var _ database.Database = &DB{}

// DB represents a SQL database with expanded features.
// so that it can properly store custom BigDipper-related data.
type DB struct {
	*mysql.Database
}

// BlockSyncerDBBuilder allows to create a new DB instance implementing the db.Builder type
func BlockSyncerDBBuilder(ctx *database.Context) (database.Database, error) {
	db, err := sqlclient.New(&ctx.Cfg)
	if err != nil {
		return nil, err
	}
	return &DB{
		Database: &mysql.Database{
			Impl: database.Impl{
				Db:             db,
				EncodingConfig: ctx.EncodingConfig,
			},
		},
	}, nil
}

// Cast allows to cast the given db to a DB instance
func Cast(db database.Database) *DB {
	bdDatabase, ok := db.(*DB)
	if !ok {
		panic(fmt.Errorf("given database instance is not a DB"))
	}
	return bdDatabase
}

// errIsNotFound check if the error is not found
func errIsNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound)
}

func (db *DB) ignoreMissingIndexDrop(err error, tableName string, model schema.Tabler) error {
	var mysqlErr *driver_mysql.MySQLError
	if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1091 || !strings.Contains(mysqlErr.Message, "Can't DROP") {
		return err
	}
	if verifyErr := db.verifyMigratedColumns(tableName, model); verifyErr != nil {
		return errors.Join(err, verifyErr)
	}
	log.Warnw("ignore missing index during schema migration after column verification", "table", tableName, "error", err)
	return nil
}

func (db *DB) verifyMigratedColumns(tableName string, model schema.Tabler) error {
	stmt := &gorm.Statement{DB: db.Db}
	if err := stmt.Parse(model); err != nil {
		return fmt.Errorf("parse model schema for %s: %w", tableName, err)
	}
	m := db.Db.Table(tableName).Migrator()
	if !m.HasTable(tableName) {
		return fmt.Errorf("table %s does not exist after schema migration", tableName)
	}
	for _, field := range stmt.Schema.Fields {
		if field.DBName == "" || field.IgnoreMigration || field.DataType == "" {
			continue
		}
		if !m.HasColumn(tableName, field.DBName) {
			return fmt.Errorf("column %s.%s does not exist after schema migration", tableName, field.DBName)
		}
	}
	return nil
}

func (db *DB) AutoMigrate(ctx context.Context, tables []schema.Tabler) error {
	q := db.Db.WithContext(ctx)
	m := db.Db.Migrator()
	for _, t := range tables {
		if t.TableName() == bsdb.PrefixTreeTableName || t.TableName() == bsdb.ObjectTableName {
			for i := 0; i < bsdb.ObjectsNumberOfShards; i++ {
				shardTableName := fmt.Sprintf(t.TableName()+"_%02d", i)
				if err := db.ignoreMissingIndexDrop(q.Table(shardTableName).AutoMigrate(t), shardTableName, t); err != nil {
					log.Errorw("migrate table failed", "table", t.TableName(), "err", err)
					return err
				}
			}
		} else {
			if err := db.ignoreMissingIndexDrop(m.AutoMigrate(t), t.TableName(), t); err != nil {
				log.Errorw("migrate table failed", "table", t.TableName(), "err", err)
				return err
			}
		}
	}
	return nil
}

func (db *DB) PrepareTables(ctx context.Context, tables []schema.Tabler) error {
	q := db.Db.WithContext(ctx)
	m := db.Db.Migrator()

	for _, t := range tables {
		if t.TableName() == bsdb.PrefixTreeTableName || t.TableName() == bsdb.ObjectTableName {
			for i := 0; i < bsdb.ObjectsNumberOfShards; i++ {
				shardTableName := fmt.Sprintf(t.TableName()+"_%02d", i)
				if m.HasTable(shardTableName) {
					continue
				}
				if err := q.Table(shardTableName).AutoMigrate(t); err != nil {
					log.Errorw("migrate table failed", "table", shardTableName, "err", err)
					return err
				}
			}
		} else {
			if m.HasTable(t.TableName()) {
				continue
			}
			if err := q.Table(t.TableName()).AutoMigrate(t); err != nil {
				log.Errorw("migrate table failed", "table", t.TableName(), "err", err)
				return err
			}
		}
	}

	return nil
}
