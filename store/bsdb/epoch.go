package bsdb

// GetEpoch get the current epoch
func (b *BsDBImpl) GetEpoch() (*Epoch, error) {
	var (
		query string
		epoch Epoch
	)

	query = "SELECT * FROM epoch LIMIT 1;"
	result := b.db.Raw(query).Scan(&epoch)

	if result.Error != nil {
		return nil, result.Error
	}

	// If no rows found, return nil
	if result.RowsAffected == 0 {
		return nil, nil
	}

	return &epoch, nil
}
