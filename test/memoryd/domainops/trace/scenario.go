package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const (
	scenarioID              = "r7-domain-ops-live"
	scenarioIdentityVersion = "memory.r7.domain-ops.scenario/v2"
	maxScenarioFileBytes    = 1 << 20
	maxBinaryManifestBytes  = 4 << 10
)

var scenarioFiles = []string{
	"test/memoryd/domainops/Dockerfile",
	"test/memoryd/domainops/run_live.sh",
	"testdata/memoryd/domainops/README.md",
	"testdata/memoryd/domainops/cmd/domain-load/main.go",
	"testdata/memoryd/domainops/cmd/domain-world/main.go",
	"testdata/memoryd/domainops/cmd/domainctl/main.go",
	"testdata/memoryd/domainops/cmd/domainctl/main_test.go",
	"testdata/memoryd/domainops/compose.yaml",
	"testdata/memoryd/domainops/domains/data/AGENTS.md",
	"testdata/memoryd/domainops/domains/edge/AGENTS.md",
	"testdata/memoryd/domainops/domains/lead/AGENTS.md",
	"testdata/memoryd/domainops/domains/payment/AGENTS.md",
	"testdata/memoryd/domainops/domains/platform/AGENTS.md",
	"testdata/memoryd/domainops/mission.md",
	"testdata/memoryd/domainops/nodes.txt",
	"testdata/memoryd/domainops/world/callback.go",
	"testdata/memoryd/domainops/world/gateway.go",
	"testdata/memoryd/domainops/world/ledger.go",
	"testdata/memoryd/domainops/world/monitor.go",
	"testdata/memoryd/domainops/world/monitor_limit_test.go",
	"testdata/memoryd/domainops/world/payment.go",
	"testdata/memoryd/domainops/world/protocol.go",
	"testdata/memoryd/domainops/world/protocol_test.go",
}

var candidateBinaryPaths = []string{
	"/opt/memory/pi-delegate/delegate-runtime.mjs",
	"/opt/memory/pi-delegate/delegate.ts",
	"/usr/local/bin/domainctl",
	"/usr/local/bin/memory",
}

type scenarioEvidence struct {
	Digest   string
	Files    []contentIdentity
	Binaries []contentIdentity
}

type contentIdentity struct {
	Name   string
	Digest string
	Bytes  int64
}

func loadScenarioEvidence(root, binaryManifestPath string) (scenarioEvidence, error) {
	files, err := hashScenarioFiles(root)
	if err != nil {
		return scenarioEvidence{}, err
	}
	binaries, err := loadCandidateBinaryManifest(binaryManifestPath)
	if err != nil {
		return scenarioEvidence{}, err
	}
	return scenarioEvidence{Digest: digestScenario(files, binaries), Files: files,
		Binaries: binaries}, nil
}

func hashScenarioFiles(root string) ([]contentIdentity, error) {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("scenario root is not a real directory")
	}
	result := make([]contentIdentity, 0, len(scenarioFiles))
	for _, relative := range scenarioFiles {
		path := filepath.Join(root, filepath.FromSlash(relative))
		identity, err := hashRegularFile(path, maxScenarioFileBytes)
		if err != nil {
			return nil, fmt.Errorf("scenario input %s: %w", relative, err)
		}
		identity.Name = relative
		result = append(result, identity)
	}
	if err := rejectUnboundScenarioFixtures(root); err != nil {
		return nil, err
	}
	return result, nil
}

func rejectUnboundScenarioFixtures(root string) error {
	const fixturePrefix = "testdata/memoryd/domainops/"
	expected := make(map[string]struct{})
	for _, path := range scenarioFiles {
		if strings.HasPrefix(path, fixturePrefix) {
			expected[path] = struct{}{}
		}
	}
	fixtureRoot := filepath.Join(root, filepath.FromSlash(strings.TrimSuffix(fixturePrefix, "/")))
	return filepath.WalkDir(fixtureRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return errors.New("scenario fixture tree contains an unsupported entry")
		}
		relative = filepath.ToSlash(relative)
		if _, bound := expected[relative]; !bound {
			return fmt.Errorf("scenario fixture %s is not bound into the scenario digest", relative)
		}
		return nil
	})
}

func hashRegularFile(path string, maximum int64) (contentIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return contentIdentity{}, errors.New("input is not a regular file")
	}
	if info.Size() < 1 || info.Size() > maximum {
		return contentIdentity{}, fmt.Errorf("input size is outside 1..%d bytes", maximum)
	}
	file, err := os.Open(path)
	if err != nil {
		return contentIdentity{}, err
	}
	defer file.Close()
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, maximum+1))
	if err != nil {
		return contentIdentity{}, err
	}
	if written != info.Size() {
		return contentIdentity{}, errors.New("input changed while being hashed")
	}
	return contentIdentity{Digest: "sha256:" + hex.EncodeToString(hash.Sum(nil)),
		Bytes: written}, nil
}

func loadCandidateBinaryManifest(path string) ([]contentIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() < 1 || info.Size() > maxBinaryManifestBytes {
		return nil, errors.New("candidate binary manifest is not a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	result := make([]contentIdentity, 0, len(candidateBinaryPaths))
	seen := make(map[string]struct{}, len(candidateBinaryPaths))
	scanner := bufio.NewScanner(io.LimitReader(file, maxBinaryManifestBytes+1))
	for scanner.Scan() {
		identity, err := parseBinaryIdentity(scanner.Text())
		if err != nil {
			return nil, err
		}
		if !slices.Contains(candidateBinaryPaths, identity.Name) {
			return nil, fmt.Errorf("candidate manifest names unexpected binary %q", identity.Name)
		}
		if _, duplicate := seen[identity.Name]; duplicate {
			return nil, fmt.Errorf("candidate manifest repeats binary %q", identity.Name)
		}
		seen[identity.Name] = struct{}{}
		result = append(result, identity)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(result) != len(candidateBinaryPaths) {
		return nil, errors.New("candidate manifest does not identify every required binary")
	}
	slices.SortFunc(result, func(left, right contentIdentity) int {
		return strings.Compare(left.Name, right.Name)
	})
	return result, nil
}

func parseBinaryIdentity(line string) (contentIdentity, error) {
	fields := strings.Fields(line)
	if len(fields) != 2 || len(fields[0]) != 64 {
		return contentIdentity{}, errors.New("candidate manifest line is not canonical sha256sum output")
	}
	if _, err := hex.DecodeString(fields[0]); err != nil || fields[0] != strings.ToLower(fields[0]) ||
		!filepath.IsAbs(fields[1]) || filepath.Clean(fields[1]) != fields[1] {
		return contentIdentity{}, errors.New("candidate manifest line has invalid digest or path")
	}
	return contentIdentity{Name: fields[1], Digest: "sha256:" + fields[0]}, nil
}

func digestScenario(files, binaries []contentIdentity) string {
	hash := sha256.New()
	writeScenarioDigestPart(hash, scenarioIdentityVersion)
	for _, identity := range files {
		writeScenarioDigestPart(hash, "file", identity.Name, identity.Digest,
			strconv.FormatInt(identity.Bytes, 10))
	}
	for _, identity := range binaries {
		writeScenarioDigestPart(hash, "binary", identity.Name, identity.Digest)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func writeScenarioDigestPart(destination io.Writer, values ...string) {
	for _, value := range values {
		_, _ = io.WriteString(destination, value)
		_, _ = destination.Write([]byte{0})
	}
}
