// Copyright 2015 The rkt Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ACIInfo is used to store information about an ACI.
type ACIInfo struct {
	// BlobKey is the key in the blob/imageManifest store of the related
	// ACI file and is the db primary key.
	BlobKey string
	// Name is the name of the ACI.
	Name string
	// ImportTime is the time this ACI was imported in the store.
	ImportTime time.Time
	// LastUsedTime is the last time this image was read
	LastUsedTime time.Time
	// Latest defines if the ACI was imported using the latest pattern (no
	// version label was provided on ACI discovery)
	Latest bool
}

func NewACIInfo(blobKey string, latest bool, t time.Time) *ACIInfo {
	return &ACIInfo{
		BlobKey:      blobKey,
		Latest:       latest,
		ImportTime:   t,
		LastUsedTime: time.Now(),
	}
}

func aciinfoRowScan(rows *sql.Rows, aciinfo *ACIInfo) error {
	// This ordering MUST match that in schema.go
	return rows.Scan(&aciinfo.BlobKey, &aciinfo.Name, &aciinfo.ImportTime, &aciinfo.LastUsedTime, &aciinfo.Latest)
}

// GetAciInfosWithKeyPrefix returns all the ACIInfos with a blobkey starting with the given prefix.
func GetACIInfosWithKeyPrefix(tx *sql.Tx, prefix string) ([]*ACIInfo, error) {
	var aciinfos []*ACIInfo
	rows, err := tx.Query("SELECT * from aciinfo WHERE hasPrefix(blobkey, $1)", prefix)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		aciinfo := &ACIInfo{}
		if err := aciinfoRowScan(rows, aciinfo); err != nil {
			return nil, err
		}
		aciinfos = append(aciinfos, aciinfo)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return aciinfos, err
}

// GetAciInfosWithName returns all the ACIInfos for a given name. found will be
// false if no aciinfo exists.
func GetACIInfosWithName(tx *sql.Tx, name string) ([]*ACIInfo, bool, error) {
	var aciinfos []*ACIInfo
	found := false
	rows, err := tx.Query("SELECT * from aciinfo WHERE name == $1", name)
	if err != nil {
		return nil, false, err
	}
	for rows.Next() {
		found = true
		aciinfo := &ACIInfo{}
		if err := aciinfoRowScan(rows, aciinfo); err != nil {
			return nil, false, err
		}
		aciinfos = append(aciinfos, aciinfo)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return aciinfos, found, err
}

// GetAciInfosWithBlobKey returns the ACIInfo with the given blobKey. found will be
// false if no aciinfo exists.
func GetACIInfoWithBlobKey(tx *sql.Tx, blobKey string) (*ACIInfo, bool, error) {
	aciinfo := &ACIInfo{}
	found := false
	rows, err := tx.Query("SELECT * from aciinfo WHERE blobkey == $1", blobKey)
	if err != nil {
		return nil, false, err
	}
	for rows.Next() {
		found = true
		if err := aciinfoRowScan(rows, aciinfo); err != nil {
			return nil, false, err
		}
		// No more than one row for blobkey must exist.
		break
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return aciinfo, found, err
}

// GetAllACIInfos returns all the ACIInfos sorted by optional sortfields and
// with ascending or descending order.
func GetAllACIInfos(tx *sql.Tx, sortfields []string, ascending bool) ([]*ACIInfo, error) {
	var aciinfos []*ACIInfo
	query := "SELECT * from aciinfo"
	if len(sortfields) > 0 {
		query += fmt.Sprintf(" ORDER BY %s ", strings.Join(sortfields, ", "))
		if ascending {
			query += "ASC"
		} else {
			query += "DESC"
		}
	}
	rows, err := tx.Query(query)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		aciinfo := &ACIInfo{}
		if err := aciinfoRowScan(rows, aciinfo); err != nil {
			return nil, err
		}
		aciinfos = append(aciinfos, aciinfo)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return aciinfos, err
}

// WriteACIInfo adds or updates the provided aciinfo.
func WriteACIInfo(tx *sql.Tx, aciinfo *ACIInfo) error {
	// ql doesn't have an INSERT OR UPDATE function so
	// it's faster to remove and reinsert the row
	_, err := tx.Exec("DELETE from aciinfo where blobkey == $1", aciinfo.BlobKey)
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT into aciinfo (blobkey, name, importtime, lastusedtime, latest) VALUES ($1, $2, $3, $4, $5)", aciinfo.BlobKey, aciinfo.Name, aciinfo.ImportTime, aciinfo.LastUsedTime, aciinfo.Latest)
	if err != nil {
		return err
	}

	return nil
}

// RemoveACIInfo removes the ACIInfo with the given blobKey.
func RemoveACIInfo(tx *sql.Tx, blobKey string) error {
	_, err := tx.Exec("DELETE from aciinfo where blobkey == $1", blobKey)
	if err != nil {
		return err
	}
	return nil
}
