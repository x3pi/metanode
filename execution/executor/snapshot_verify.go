package executor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// VerifySnapshotIntegrity verifies the checksums in a snapshot's metadata.json
func VerifySnapshotIntegrity(snapshotPath string) (*SnapshotMetadata, error) {
	metadataPath := filepath.Join(snapshotPath, "metadata.json")
	metadataBytes, err := os.ReadFile(metadataPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read metadata.json: %w", err)
	}

	var metadata SnapshotMetadata
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		return nil, fmt.Errorf("failed to unmarshal metadata.json: %w", err)
	}

	// 1. Verify Metadata Checksum itself
	// To do this, we must clone metadata, clear the MetadataChecksum field, marshal it, and hash it
	metadataCopy := metadata
	expectedMetadataChecksum := metadataCopy.MetadataChecksum
	metadataCopy.MetadataChecksum = ""

	metadataCopyBytes, _ := json.Marshal(metadataCopy)
	hash := sha256.Sum256(metadataCopyBytes)
	actualMetadataChecksum := hex.EncodeToString(hash[:])

	if expectedMetadataChecksum != actualMetadataChecksum {
		return &metadata, fmt.Errorf("metadata checksum mismatch! expected %s, got %s", expectedMetadataChecksum, actualMetadataChecksum)
	}

	// 2. Verify Critical Directories
	for dirName, expectedChecksum := range metadata.CriticalChecksums {
		dirPath := filepath.Join(snapshotPath, dirName)
		if _, err := os.Stat(dirPath); os.IsNotExist(err) {
			return &metadata, fmt.Errorf("critical directory missing: %s", dirName)
		}

		actualChecksum, err := calculateDirectoryChecksum(dirPath)
		if err != nil {
			return &metadata, fmt.Errorf("failed to calculate checksum for %s: %w", dirName, err)
		}

		if expectedChecksum != actualChecksum {
			return &metadata, fmt.Errorf("directory checksum mismatch for %s! expected %s, got %s", dirName, expectedChecksum, actualChecksum)
		}
	}

	return &metadata, nil
}

// calculateDirectoryChecksum calculates a deterministic SHA-256 hash of all file contents in a directory
func calculateDirectoryChecksum(dir string) (string, error) {
	var files []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			relPath, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			files = append(files, relPath)
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	// Sort to ensure deterministic order
	sort.Strings(files)

	hash := sha256.New()
	for _, relPath := range files {
		// Include file path in hash so renames change the hash
		hash.Write([]byte(relPath))

		filePath := filepath.Join(dir, relPath)
		f, err := os.Open(filePath)
		if err != nil {
			return "", err
		}

		if _, err := io.Copy(hash, f); err != nil {
			f.Close()
			return "", err
		}
		f.Close()
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}
