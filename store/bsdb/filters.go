package bsdb

import (
	"github.com/forbole/juno/v4/common"
	"gorm.io/gorm"
)

func ContinuationTokenFilter(continuationToken string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("object_name >= ?", continuationToken)
	}
}

func PrefixFilter(prefix string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("object_name LIKE ?", prefix+"%")
	}
}

func PathNameFilter(pathName string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("path_name = ?", pathName)
	}
}

func NameFilter(name string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("name like ?", name+"%")
	}
}

func FullNameFilter(fullName string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("full_name >= ?", fullName)
	}
}

func SourceTypeFilter(sourceType string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("source_type = ?", sourceType)
	}
}

func RemovedFilter(removed bool) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("removed = ?", removed)
	}
}

func BucketIDStartAfterFilter(bucketID common.Hash) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("bucket_id > ?", bucketID)
	}
}

func ObjectIDStartAfterFilter(objectID common.Hash) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("object_id > ?", objectID)
	}
}

func GroupIDStartAfterFilter(groupID common.Hash) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("group_id > ?", groupID)
	}
}

// GroupAccountIDStartAfterFilter
// In the "group" table, each group has an account ID of "0x0000000000000000000000000000000000000000" representing the group's creation information.
// Since the "group" table maps groups to account, this special value is used to filter out non-user data
func GroupAccountIDStartAfterFilter(accountID common.Address) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("account_id > ? and account_id != ?", accountID, common.HexToAddress("0"))
	}
}

func CreateAtFilter(createAt int64) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("create_at <= ?", createAt)
	}
}

func CreateAtEqualFilter(createAt int64) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("create_at = ?", createAt)
	}
}

func WithLimit(limit int) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Limit(limit)
	}
}

// PublicReadOnlyFilter restricts a query on an objects table to the objects a caller
// with no relationship to the bucket may see: visibility PUBLIC_READ, or INHERIT when
// the parent bucket is public. It matches the predicate the single-object
// GetObjectByName(includePrivate=false) path already uses.
func PublicReadOnlyFilter(bucketName string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("visibility = 'VISIBILITY_TYPE_PUBLIC_READ' or "+
			"(visibility = 'VISIBILITY_TYPE_INHERIT' and exists "+
			"(select 1 from buckets where buckets.bucket_name = ? and buckets.visibility = 'VISIBILITY_TYPE_PUBLIC_READ'))",
			bucketName)
	}
}
