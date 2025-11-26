package sqlstorage

import (
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/russross/meddler"
)

func initMeddler() {
	meddler.Default = meddler.SQLite
	meddler.Debug = false
	meddler.Register("address", AddressMeddler{})
	meddler.Register("bigInt", BigIntMeddler{})
	meddler.Register("hash", HashMeddler{})
	meddler.Register("timeRFC3339", TimeRFC3339Meddler{})
}

// AddressMeddler encodes or decodes the field value to or from JSON.
type AddressMeddler struct{}

// PreRead is called before a Scan operation for fields that have the AddressMeddler.
func (m AddressMeddler) PreRead(fieldAddr interface{}) (scanTarget interface{}, err error) {
	// Return a new sql.NullString pointer to handle potential NULL values.
	return new(sql.NullString), nil
}

// PostRead is called after a Scan operation for fields that have the AddressMeddler.
func (m AddressMeddler) PostRead(fieldPtr, scanTarget interface{}) error {
	nullStrPtr, ok := scanTarget.(*sql.NullString)
	if !ok {
		return errors.New("scanTarget is not *sql.NullString")
	}

	// Handle both *common.Address and **common.Address cases
	switch addr := fieldPtr.(type) {
	case *common.Address:
		// If fieldPtr is a pointer to a common.Address, set its value.
		if addr == nil {
			return errors.New("AddressMeddler.PostRead: fieldPtr is nil *common.Address")
		}
		if nullStrPtr.Valid {
			*addr = common.HexToAddress(nullStrPtr.String)
		} else {
			*addr = common.Address{} // Reset the address to zero value if NULL
		}

	case **common.Address:
		// If fieldPtr is a pointer to a *common.Address (double pointer), allocate memory if nil.
		if nullStrPtr.Valid {
			if *addr == nil {
				*addr = new(common.Address)
			}
			**addr = common.HexToAddress(nullStrPtr.String)
		} else {
			*addr = nil // Set to nil if the value is NULL
		}

	default:
		return errors.New("fieldPtr is neither *common.Address nor **common.Address")
	}

	return nil
}

// PreWrite is called before an Insert or Update operation for fields that have the AddressMeddler.
func (m AddressMeddler) PreWrite(fieldPtr interface{}) (saveValue interface{}, err error) {
	// Handle both common.Address and *common.Address cases.
	switch addr := fieldPtr.(type) {
	case common.Address:
		// Handle the non-pointer case.
		return addr.Hex(), nil

	case *common.Address:
		// Handle the pointer case, check for nil to avoid dereferencing.
		if addr == nil {
			return nil, nil // Save NULL to the DB if the address is nil
		}
		return addr.Hex(), nil

	default:
		return nil, errors.New("fieldPtr is neither common.Address nor *common.Address")
	}
}

// BigIntMeddler encodes or decodes a *big.Int field to/from a string,
// handling both decimal and hexadecimal representations.
type BigIntMeddler struct{}

// PreRead is called before a Scan operation for fields that have the BigIntMeddler.
// It gives a pointer to a string buffer to grab the raw data from the database.
func (m BigIntMeddler) PreRead(fieldAddr interface{}) (scanTarget interface{}, err error) {
	// Return a pointer to a string to scan the raw data.
	return new(sql.NullString), nil
}

// PostRead is called after a Scan operation for fields that have the BigIntMeddler.
// It converts the sql.NullString or NULL from the database into a *big.Int.
func (m BigIntMeddler) PostRead(fieldPtr, scanTarget interface{}) error {
	nullStr, ok := scanTarget.(*sql.NullString)
	if !ok {
		return errors.New("scanTarget is not *sql.NullString")
	}

	// If the database returned NULL, set the field to nil.
	field, ok := fieldPtr.(**big.Int)
	if !ok {
		return errors.New("fieldPtr is not **big.Int")
	}

	// If the NullString is valid and not empty, parse the value into *big.Int
	if nullStr.Valid {
		parsedInt := new(big.Int)
		_, ok = parsedInt.SetString(nullStr.String, 0) // 0 allows automatic base detection
		if !ok {
			return fmt.Errorf("big.Int.SetString failed on value \"%v\"", nullStr.String)
		}
		*field = parsedInt
	} else {
		*field = nil // Set to nil if the database value is NULL
	}

	return nil
}

// PreWrite is called before an Insert or Update operation for fields that have the BigIntMeddler.
// It converts the *big.Int field into a string for storage in the database or returns nil for NULL.
func (m BigIntMeddler) PreWrite(fieldPtr interface{}) (saveValue interface{}, err error) {
	field, ok := fieldPtr.(*big.Int)
	if !ok {
		return nil, errors.New("fieldPtr is not *big.Int")
	}

	// If the field is nil, return nil to store NULL in the database.
	if field == nil {
		return nil, nil
	}

	// Return the string representation of the *big.Int
	return field.String(), nil
}

// HashMeddler encodes or decodes the field value to or from string
type HashMeddler struct{}

// PreRead is called before a Scan operation for fields that have the HashMeddler
func (m HashMeddler) PreRead(fieldAddr interface{}) (scanTarget interface{}, err error) {
	// give a pointer to a byte buffer to grab the raw data
	_, ok := fieldAddr.(**common.Hash)
	if ok {
		// This is because if not the rows.Scan fails 'converting NULL to string is unsupported'
		return new(*string), nil
	}
	return new(string), nil
}
func (m HashMeddler) postReadDoublePtr(fieldPtr, scanTarget interface{}) error {
	rawHashPtr, ok := scanTarget.(**string)
	if !ok {
		return errors.New("scanTarget is not **string")
	}
	// Handle the case where fieldPtr is a **common.Hash (nullable field)
	hashPtr, ok := fieldPtr.(**common.Hash)
	if ok {
		if rawHashPtr == nil || *rawHashPtr == nil {
			return nil
		}
		// If the string is empty, set the hash to nil
		if len(**rawHashPtr) == 0 {
			*hashPtr = nil
			// Otherwise, convert the string to a common.Hash and assign it
		} else {
			tmp := common.HexToHash(**rawHashPtr)
			*hashPtr = &tmp
		}
		return nil
	}
	return errors.New("fieldPtr is not **common.Hash")
}

// PostRead is called after a Scan operation for fields that have the HashMeddler
func (m HashMeddler) PostRead(fieldPtr, scanTarget interface{}) error {
	_, ok := scanTarget.(**string)
	if ok {
		return m.postReadDoublePtr(fieldPtr, scanTarget)
	}
	rawHashPtr, ok := scanTarget.(*string)
	if !ok {
		return errors.New("scanTarget is not *string")
	}

	// Handle the case where fieldPtr is a *common.Hash
	field, ok := fieldPtr.(*common.Hash)
	if ok {
		*field = common.HexToHash(*rawHashPtr)
		return nil
	}
	// If fieldPtr is neither a *common.Hash, return an error
	return errors.New("fieldPtr is not *common.Hash")
}

// PreWrite is called before an Insert or Update operation for fields that have the HashMeddler
func (m HashMeddler) PreWrite(fieldPtr interface{}) (saveValue interface{}, err error) {
	field, ok := fieldPtr.(common.Hash)
	if !ok {
		hashPtr, ok := fieldPtr.(*common.Hash)
		if !ok {
			return nil, errors.New("fieldPtr is not common.Hash")
		}
		if hashPtr == nil {
			return []byte{}, nil
		}
		return hashPtr.Hex(), nil
	}
	return field.Hex(), nil
}

// TimeRFC3339Meddler encodes or decodes time.Time to/from a consistent RFC3339 format for the database.
type TimeRFC3339Meddler struct{}

// PreRead is called before a Scan operation for fields that have the TimeRFC3339Meddler.
func (m TimeRFC3339Meddler) PreRead(fieldAddr interface{}) (scanTarget interface{}, err error) {
	// We use a pointer to sql.NullString to read the raw time as an RFC3339 string or NULL from the database.
	return new(sql.NullString), nil
}

// PostRead is called after a Scan operation for fields that have the TimeRFC3339Meddler.
func (m TimeRFC3339Meddler) PostRead(fieldPtr, scanTarget interface{}) error {
	nullStr, ok := scanTarget.(*sql.NullString)
	if !ok {
		return errors.New("scanTarget is not *sql.NullString")
	}

	// Handle NULL values from the database.
	field, ok := fieldPtr.(*time.Time)
	if !ok {
		return errors.New("fieldPtr is not *time.Time")
	}

	// If the value is NULL (invalid), set the time to the zero value.
	if !nullStr.Valid || nullStr.String == "" {
		*field = time.Time{} // Zero value for time.Time
		return nil
	}

	// Parse the string value as an RFC3339 formatted time.
	parsedTime, err := time.Parse(time.RFC3339, nullStr.String)
	if err != nil {
		return fmt.Errorf("failed to parse time in RFC3339 format: %w", err)
	}

	// Assign the parsed time to the time.Time field.
	*field = parsedTime
	return nil
}

// PreWrite is called before an Insert or Update operation for fields that have the TimeRFC3339Meddler.
func (m TimeRFC3339Meddler) PreWrite(fieldPtr interface{}) (saveValue interface{}, err error) {
	field, ok := fieldPtr.(time.Time)
	if !ok {
		return nil, errors.New("fieldPtr is not time.Time")
	}

	// If the time is the zero value, return nil so that NULL is stored in the DB.
	if field.IsZero() {
		return nil, nil // Save NULL to the DB for zero time.
	}

	// Ensure that time is written in a consistent RFC3339 format without extra precision.
	// We use field.Truncate(time.Microsecond) to avoid inconsistencies in time precision.
	return field.Truncate(time.Microsecond).Format(time.RFC3339), nil
}
