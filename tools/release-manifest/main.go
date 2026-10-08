package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

type asset struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type manifest struct {
	Version string           `json:"version"`
	Assets  map[string]asset `json:"assets"`
}

func main() {
	directory := flag.String("dir", "dist", "release asset directory")
	version := flag.String("version", "", "release version")
	generate := flag.Bool("generate-key", false, "generate an Ed25519 release key pair")
	flag.Parse()
	if *generate {
		publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
		must(err)
		value := map[string]string{
			"private_key": base64.StdEncoding.EncodeToString(privateKey),
			"public_key":  base64.StdEncoding.EncodeToString(publicKey),
		}
		encoded, err := json.MarshalIndent(value, "", "  ")
		must(err)
		fmt.Println(string(encoded))
		return
	}
	if *version == "" {
		fatal("-version is required")
	}
	encodedKey := os.Getenv("SQ_RELEASE_PRIVATE_KEY")
	privateKey, err := base64.StdEncoding.DecodeString(encodedKey)
	if err != nil || len(privateKey) != ed25519.PrivateKeySize {
		fatal("SQ_RELEASE_PRIVATE_KEY must be a base64 Ed25519 private key")
	}
	result := manifest{Version: *version, Assets: map[string]asset{}}
	for _, name := range []string{
		"sqprobe-linux-amd64", "sqprobe-linux-arm64",
		"sq-node-linux-amd64", "sq-node-linux-arm64",
	} {
		path := filepath.Join(*directory, name)
		data, readErr := os.ReadFile(path)
		must(readErr)
		info, statErr := os.Stat(path)
		must(statErr)
		digest := sha256.Sum256(data)
		result.Assets[name] = asset{SHA256: hex.EncodeToString(digest[:]), Size: info.Size()}
	}
	contents, err := json.MarshalIndent(result, "", "  ")
	must(err)
	contents = append(contents, '\n')
	signature := ed25519.Sign(ed25519.PrivateKey(privateKey), contents)
	must(os.WriteFile(filepath.Join(*directory, "release-manifest.json"), contents, 0o644))
	must(os.WriteFile(
		filepath.Join(*directory, "release-manifest.json.sig"),
		[]byte(base64.StdEncoding.EncodeToString(signature)+"\n"),
		0o644,
	))
}

func must(err error) {
	if err != nil {
		fatal(err.Error())
	}
}

func fatal(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
