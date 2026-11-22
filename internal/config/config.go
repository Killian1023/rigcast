package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"rigcast/internal/models"
)

type Client struct {
	Host  string `yaml:"host"`
	Token string `yaml:"token"`
}

func Global() (Client, error) {
	var c Client
	c.Host, c.Token = os.Getenv("RIGCAST_HOST"), os.Getenv("RIGCAST_TOKEN")
	if c.Host != "" && c.Token != "" {
		return c, nil
	}
	dir, err := globalDir()
	if err != nil {
		return c, err
	}
	var file Client
	f, err := os.Open(filepath.Join(dir, "config.yaml"))
	if err == nil {
		defer f.Close()
		err = decode(f, &file)
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return c, fmt.Errorf("read global config: %w", err)
	}
	if c.Host == "" {
		c.Host = file.Host
	}
	if c.Token == "" {
		c.Token = file.Token
	}
	if strings.TrimSpace(c.Host) == "" || strings.TrimSpace(c.Token) == "" {
		return c, errors.New("set RIGCAST_HOST and RIGCAST_TOKEN or configure " + filepath.Join(dir, "config.yaml"))
	}
	return c, nil
}

func globalDir() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(dir) {
		return filepath.Join(dir, "rigcast"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "rigcast"), nil
}

func Project(root *os.Root) (models.Spec, error) {
	spec := models.Spec{GPUs: "all", Workdir: "/workspace"}
	f, err := root.Open("rigcast.yaml")
	if errors.Is(err, fs.ErrNotExist) {
		return spec, nil
	}
	if err == nil {
		defer f.Close()
		err = decode(f, &spec)
	}
	if err != nil {
		return models.Spec{}, fmt.Errorf("read rigcast.yaml: %w", err)
	}
	return spec, nil
}

func decode(r io.Reader, dst any) error {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	err := dec.Decode(dst)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return errors.New("expected one YAML document")
	}
	return nil
}

const starter = `image: "pytorch/pytorch:2.4.0-cuda12.4-cudnn9-runtime"
gpus: "all"
datasets: []
env:
  PYTHONUNBUFFERED: "1"
workdir: "/workspace"
# timeout: "4h"
# memory_limit: "32g"
`

func Init(dir string) error {
	f, err := os.OpenFile(filepath.Join(dir, "rigcast.yaml"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	_, err = io.WriteString(f, starter)
	return errors.Join(err, f.Close())
}
