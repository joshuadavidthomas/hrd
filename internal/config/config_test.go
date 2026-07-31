package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadUsesInstallDefaultWhenConfigIsMissing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.HerdrConfigSync != HerdrConfigSyncInstall {
		t.Fatalf("HerdrConfigSync = %v, want install", got.HerdrConfigSync)
	}
}

func TestLoadHerdrConfigSync(t *testing.T) {
	for _, test := range []struct {
		value string
		want  HerdrConfigSync
	}{
		{value: "never", want: HerdrConfigSyncNever},
		{value: "install", want: HerdrConfigSyncInstall},
		{value: "open", want: HerdrConfigSyncOpen},
	} {
		t.Run(test.value, func(t *testing.T) {
			configHome := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", configHome)
			writeConfig(t, configHome, "herdr_config_sync = \""+test.value+"\"\n")
			got, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if got.HerdrConfigSync != test.want {
				t.Fatalf("HerdrConfigSync = %v, want %v", got.HerdrConfigSync, test.want)
			}
		})
	}
}

func TestLoadRejectsInvalidAndUnknownSettings(t *testing.T) {
	for _, contents := range []string{
		"herdr_config_sync = \"\"\n",
		"herdr_config_sync = \"always\"\n",
		"surprise = true\n",
	} {
		t.Run(strings.TrimSpace(contents), func(t *testing.T) {
			configHome := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", configHome)
			writeConfig(t, configHome, contents)
			if _, err := Load(); err == nil {
				t.Fatal("Load() accepted invalid config")
			}
		})
	}
}

func TestLoadRejectsRelativeConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "relative")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted relative XDG_CONFIG_HOME")
	}
}

func writeConfig(t *testing.T, configHome, contents string) {
	t.Helper()
	directory := filepath.Join(configHome, "hrd")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "config.toml"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
