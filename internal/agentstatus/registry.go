// Package agentstatus stores host-owned run identity separately from untrusted reports.
package agentstatus

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const RunLabel = "wisp.run-id"
const MountTarget = "/run/wisp/agent-status"
const maxFileSize = 16 * 1024

type Metadata struct {
	SchemaVersion  int    `json:"schema_version"`
	RunID          string `json:"run_id"`
	Repo           string `json:"repo"`
	SandboxName    string `json:"sandbox_name"`
	ProjectHash    string `json:"project_hash"`
	ComposeProject string `json:"compose_project"`
	UID            int    `json:"uid"`
}

type Report struct {
	Reporter      string `json:"-"`
	SchemaVersion int    `json:"schema_version"`
	State         string `json:"state"`
	Reason        string `json:"reason,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
}

type Entry struct {
	Metadata
	Report Report
}

type Registration struct {
	Metadata
	StatusDir string
	dir       string
}

// openDirectory walks from / using directory descriptors, never following a symlink.
func openDirectory(name string, uid int) (*os.File, error) {
	if !filepath.IsAbs(name) {
		return nil, fmt.Errorf("registry path must be absolute")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(filepath.Clean(name), "/"), "/") {
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
		unix.Close(fd)
		if e != nil {
			return nil, e
		}
		fd = next
	}
	f := os.NewFile(uintptr(fd), name)
	if err := privateDirectory(f, uid); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func privateDirectory(f *os.File, uid int) error {
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || int(st.Uid) != uid || st.Mode&0077 != 0 {
		return fmt.Errorf("registry directory must be private and owned by UID %d", uid)
	}
	return nil
}

func childDirectory(parent *os.File, name string, uid int, create bool) (*os.File, error) {
	if create {
		if err := unix.Mkdirat(int(parent.Fd()), name, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
			return nil, err
		}
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	if err := privateDirectory(f, uid); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func Register(root string, metadata Metadata) (_ Registration, resultErr error) {
	f, err := openDirectory(root, metadata.UID)
	if err != nil {
		return Registration{}, err
	}
	defer f.Close()
	agents, err := childDirectory(f, "agents", metadata.UID, true)
	if err != nil {
		return Registration{}, err
	}
	defer agents.Close()
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return Registration{}, err
	}
	metadata.RunID = hex.EncodeToString(id[:])
	metadata.SchemaVersion = 1
	if err := unix.Mkdirat(int(agents.Fd()), metadata.RunID, 0700); err != nil {
		return Registration{}, err
	}
	r := Registration{Metadata: metadata, dir: filepath.Join(root, "agents", metadata.RunID)}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, r.Cleanup())
		}
	}()
	run, err := childDirectory(agents, metadata.RunID, metadata.UID, false)
	if err != nil {
		return Registration{}, err
	}
	defer run.Close()
	status, err := childDirectory(run, "status", metadata.UID, true)
	if err != nil {
		return Registration{}, err
	}
	status.Close()
	r.StatusDir = filepath.Join(r.dir, "status")
	data, err := json.Marshal(metadata)
	if err != nil {
		return Registration{}, err
	}
	fd, err := unix.Openat(int(run.Fd()), "metadata.json", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return Registration{}, err
	}
	file := os.NewFile(uintptr(fd), "metadata.json")
	_, err = file.Write(data)
	err = errors.Join(err, file.Close())
	return r, err
}

func (r Registration) Cleanup() error { return os.RemoveAll(r.dir) }

// readJSON bounds even growing files and refuses devices, FIFOs and symlinks.
func readJSON(parent *os.File, name string, value any) error {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maxFileSize {
		return fmt.Errorf("invalid registry file")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if err != nil {
		return err
	}
	if len(data) > maxFileSize {
		return fmt.Errorf("registry file too large")
	}
	return json.Unmarshal(data, value)
}

func readReport(run *os.File, uid int) Report {
	unknown := Report{SchemaVersion: 1, State: "unknown", Reporter: "invalid"}
	status, err := childDirectory(run, "status", uid, false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			unknown.Reporter = "missing"
		}
		return unknown
	}
	defer status.Close()
	var data json.RawMessage
	if err := readJSON(status, "agent.json", &data); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			unknown.Reporter = "missing"
		}
		return unknown
	}
	var header struct {
		SchemaVersion int `json:"schema_version"`
	}
	if json.Unmarshal(data, &header) != nil || header.SchemaVersion <= 0 {
		return unknown
	}
	if header.SchemaVersion != 1 {
		unknown.Reporter = "unsupported"
		return unknown
	}
	var report Report
	if json.Unmarshal(data, &report) != nil {
		return unknown
	}
	switch report.State {
	case "idle", "working", "waiting", "done", "failed":
	default:
		return unknown
	}
	switch report.Reason {
	case "", "permission", "question", "retry":
	default:
		return unknown
	}
	if _, err := time.Parse(time.RFC3339Nano, report.UpdatedAt); err != nil {
		return unknown
	}
	report.Reporter = "ready"
	return report
}

func List(root string, uid int) ([]Entry, error) {
	f, err := openDirectory(root, uid)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	agents, err := childDirectory(f, "agents", uid, false)
	if errors.Is(err, os.ErrNotExist) {
		return []Entry{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer agents.Close()
	names, err := agents.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	entries := []Entry{}
	for _, name := range names {
		id, err := hex.DecodeString(name)
		if err != nil || len(id) != 16 {
			continue
		}
		run, err := childDirectory(agents, name, uid, false)
		if err != nil {
			continue
		}
		var m Metadata
		err = readJSON(run, "metadata.json", &m)
		if err == nil && m.SchemaVersion == 1 && m.RunID == name && m.UID == uid && filepath.IsAbs(m.Repo) && m.SandboxName != "" && m.ComposeProject != "" && len(m.ProjectHash) == 64 {
			entries = append(entries, Entry{Metadata: m, Report: readReport(run, uid)})
		}
		run.Close()
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Repo != entries[j].Repo {
			return entries[i].Repo < entries[j].Repo
		}
		return entries[i].RunID < entries[j].RunID
	})
	return entries, nil
}
