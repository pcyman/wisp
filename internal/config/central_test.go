package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCentralConfig(t *testing.T) {
	for _, test := range []struct {
		toml    string
		wantErr bool
	}{
		{"[[central.projects]]\nname='app'\npath='~/app'\n", false},
		{"[[central.projects]]\npath='~/app'\n", true},
		{"[[central.projects]]\nname='app'\n", true},
		{"[[central.projects]]\nname=''\npath='app'\n", true},
		{"[[central.projects]]\nname=\"bad\\u001b\"\npath='app'\n", true},
		{"[[central.projects]]\nname='app'\npath='app'\n[[central.projects]]\nname='app'\npath='other'\n", true},
		{"[[central.projects]]\nname='app'\npath='app'\ncommand='arbitrary'\n", true},
	} {
		input := test.toml
		raw, err := Decode([]byte("schema_version=1\n" + input))
		if err == nil {
			_, err = Resolve(raw)
		}
		if (err != nil) != test.wantErr {
			t.Errorf("%s: err=%v wantErr=%v", input, err, test.wantErr)
		}
	}
}

func TestCentralProjectsResolvePhysicalConfigRelativeAndHomePaths(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "app")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "alias")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Central: CentralConfig{Projects: []CentralProject{{Name: "relative", Path: "alias"}, {Name: "home", Path: "~/app"}}}}
	got, err := CentralProjects(cfg, filepath.Join(root, "config.toml"), Environment{Home: root})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range got {
		if entry.Path != dir {
			t.Errorf("path=%q want %q", entry.Path, dir)
		}
	}
	cfg.Central.Projects[0].Path = "missing"
	if _, err := CentralProjects(cfg, filepath.Join(root, "config.toml"), Environment{Home: root}); err == nil {
		t.Fatal("accepted missing path")
	}
	cfg.Central.Projects[0].Path = "config.toml"
	if err := os.WriteFile(filepath.Join(root, "config.toml"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CentralProjects(cfg, filepath.Join(root, "config.toml"), Environment{Home: root}); err == nil {
		t.Fatal("accepted file as directory")
	}
}
