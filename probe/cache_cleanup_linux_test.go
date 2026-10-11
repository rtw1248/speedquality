package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestLegacyCacheMigration(t *testing.T) {
	for _, scenario := range []string{"normal", "unknown", "modified", "file-link", "version-link", "root-link", "parent-link", "hardlink", "writable", "busy", "subdirectory"} {
		t.Run(scenario, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "cache")
			versionDir := filepath.Join(root, "v1.2.0")
			if err := os.MkdirAll(versionDir, 0700); err != nil {
				t.Fatal(err)
			}
			payload := []byte("published probe fixture")
			file := filepath.Join(versionDir, "sqprobe-linux-amd64")
			if err := os.WriteFile(file, payload, 0700); err != nil {
				t.Fatal(err)
			}
			known := map[string]string{"v1.2.0/sqprobe-linux-amd64": fmt.Sprintf("%x", sha256.Sum256(payload))}
			keep := ""
			wantRemoved := 0
			switch scenario {
			case "normal":
				wantRemoved = 1
			case "unknown":
				keep = filepath.Join(versionDir, "personal.txt")
				os.WriteFile(keep, []byte("keep"), 0600)
				wantRemoved = 1
			case "modified":
				os.WriteFile(file, []byte("user file with the same name"), 0700)
			case "file-link":
				keep = filepath.Join(base, "outside")
				os.Rename(file, keep)
				os.Symlink(keep, file)
			case "version-link":
				keep = filepath.Join(base, "outside")
				os.Rename(versionDir, keep)
				os.Symlink(keep, versionDir)
			case "root-link":
				keep = filepath.Join(base, "outside")
				os.Rename(root, keep)
				os.Symlink(keep, root)
			case "parent-link":
				alias := filepath.Join(base, "alias")
				os.Symlink(base, alias)
				root = filepath.Join(alias, "cache")
				keep = file
			case "hardlink":
				keep = filepath.Join(base, "outside")
				os.Link(file, keep)
			case "writable":
				os.Chmod(versionDir, 0777)
			case "subdirectory":
				os.Remove(file)
				os.Mkdir(file, 0700)
				keep = filepath.Join(file, "personal.txt")
				os.WriteFile(keep, []byte("keep"), 0600)
			}
			removed, _, _ := cleanLegacyCache(root, known, func() bool { return scenario == "busy" })
			if removed != wantRemoved {
				t.Fatalf("removed %d, want %d", removed, wantRemoved)
			}
			if keep != "" {
				if _, err := os.Lstat(keep); err != nil {
					t.Fatalf("unrelated file lost: %v", err)
				}
			}
			if scenario == "normal" {
				if _, err := os.Lstat(root); !os.IsNotExist(err) {
					t.Fatal("empty cache root remains")
				}
			} else if _, err := os.Lstat(root); err != nil {
				t.Fatalf("protected root lost: %v", err)
			}
			if wantRemoved == 0 {
				if _, err := os.Lstat(file); err != nil {
					t.Fatalf("protected file lost: %v", err)
				}
			}
		})
	}
}

func TestLegacyCacheAllPublishedVersions(t *testing.T) {
	known := legacyProbeHashes()
	if len(known) != 46 {
		t.Fatalf("expected both architectures of 23 historical releases, got %d", len(known))
	}
	root := filepath.Join(t.TempDir(), "cache")
	fixtures := make(map[string]string)
	for name := range known {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		payload := []byte(name)
		if err := os.WriteFile(path, payload, 0700); err != nil {
			t.Fatal(err)
		}
		fixtures[name] = fmt.Sprintf("%x", sha256.Sum256(payload))
	}
	removed, retained, err := cleanLegacyCache(root, fixtures, func() bool { return false })
	if err != nil || retained || removed != 46 {
		t.Fatalf("migration: %d %t %v", removed, retained, err)
	}
	if _, err = os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("cache root remains")
	}
}

func TestLegacyCacheRechecksFileBeforeDeletion(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	version := filepath.Join(root, "v1.2.0")
	os.MkdirAll(version, 0700)
	file := filepath.Join(version, "sqprobe-linux-amd64")
	os.WriteFile(file, []byte("old"), 0700)
	known := map[string]string{"v1.2.0/sqprobe-linux-amd64": fmt.Sprintf("%x", sha256.Sum256([]byte("old")))}
	calls := 0
	removed, _, err := cleanLegacyCache(root, known, func() bool {
		calls++
		if calls == 2 {
			os.Remove(file)
			os.WriteFile(file, []byte("unrelated replacement"), 0700)
		}
		return false
	})
	if err != nil || removed != 0 {
		t.Fatalf("unexpected removal: %d %v", removed, err)
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "unrelated replacement" {
		t.Fatal("replacement file lost")
	}
}

func TestLegacyBashBetweenMeasurementsDefersCleanup(t *testing.T) {
	// No sqprobe is alive: an old launcher can still need it for the next province.
	command := exec.Command("bash", "-c", "read -r value")
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { input.Close(); command.Process.Kill(); command.Wait() }()
	if !legacyTasksBusy() {
		t.Fatal("unregistered Bash task was not protected")
	}
}
