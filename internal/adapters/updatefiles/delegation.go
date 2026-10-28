package updatefiles

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// LauncherDelegation records that a bootstrap re-executed the selected version
// as the launcher. Attempts counts starts that ended before the delegated
// launcher confirmed; once they are exhausted the delegation is Failed.
type LauncherDelegation struct {
	SHA256    string `json:"sha256"`
	Confirmed bool   `json:"confirmed"`
	Attempts  int    `json:"attempts,omitempty"`
	Failed    bool   `json:"failed,omitempty"`
}

func (s *Store) LoadDelegation() (LauncherDelegation, error) {
	var delegation LauncherDelegation
	path := filepath.Join(s.root, "launcher.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return delegation, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4096 {
		return delegation, errors.New("unsafe launcher delegation state")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return delegation, err
	}
	if json.Unmarshal(data, &delegation) != nil || (delegation.SHA256 != "" && !validDigest(delegation.SHA256)) {
		return LauncherDelegation{}, errors.New("invalid launcher delegation state")
	}
	return delegation, nil
}

func (s *Store) SaveDelegation(delegation LauncherDelegation) error {
	if !validDigest(delegation.SHA256) {
		return errors.New("invalid launcher delegation digest")
	}
	data, err := json.Marshal(delegation)
	if err != nil {
		return err
	}
	return atomicWrite(s.root, "launcher.json", data)
}
