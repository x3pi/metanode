package blockchain

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEpochDataSerialization tests JSON marshaling/unmarshaling of EpochData
func TestEpochDataSerialization(t *testing.T) {
	original := EpochData{
		CurrentEpoch:          5,
		EpochStartTimestampMs: 1640995200000,
		EpochStartTimestamps: map[uint64]uint64{
			0: 1640995200000 - 86400000*5,
			1: 1640995200000 - 86400000*4,
			2: 1640995200000 - 86400000*3,
			3: 1640995200000 - 86400000*2,
			4: 1640995200000 - 86400000*1,
			5: 1640995200000,
		},
	}

	// Test marshaling
	data, err := json.Marshal(original)
	require.NoError(t, err, "Marshal should not fail")
	require.NotEmpty(t, data, "Marshaled data should not be empty")

	// Test unmarshaling
	var restored EpochData
	err = json.Unmarshal(data, &restored)
	require.NoError(t, err, "Unmarshal should not fail")

	// Verify data integrity
	assert.Equal(t, original.CurrentEpoch, restored.CurrentEpoch)
	assert.Equal(t, original.EpochStartTimestampMs, restored.EpochStartTimestampMs)
	assert.Equal(t, len(original.EpochStartTimestamps), len(restored.EpochStartTimestamps))

	for epoch, timestamp := range original.EpochStartTimestamps {
		restoredTimestamp, exists := restored.EpochStartTimestamps[epoch]
		assert.True(t, exists, "Epoch %d should exist in restored data", epoch)
		assert.Equal(t, timestamp, restoredTimestamp, "Timestamp for epoch %d should match", epoch)
	}
}

// TestEpochDataEmptyMap tests serialization with empty timestamps map
func TestEpochDataEmptyMap(t *testing.T) {
	original := EpochData{
		CurrentEpoch:          0,
		EpochStartTimestampMs: 1640995200000,
		EpochStartTimestamps:  map[uint64]uint64{},
	}

	// Test marshaling
	data, err := json.Marshal(original)
	require.NoError(t, err)

	// Test unmarshaling
	var restored EpochData
	err = json.Unmarshal(data, &restored)
	require.NoError(t, err)

	assert.Equal(t, original.CurrentEpoch, restored.CurrentEpoch)
	assert.Equal(t, original.EpochStartTimestampMs, restored.EpochStartTimestampMs)
	assert.Empty(t, restored.EpochStartTimestamps)
}

// TestAdvanceEpochLogic tests the epoch advancement logic
func TestAdvanceEpochLogic(t *testing.T) {
	// Create ChainState without storage (just test logic)
	cs := &ChainState{
		currentEpoch:          5,
		epochStartTimestampMs: 1640995200000,
		epochStartTimestamps: map[uint64]uint64{
			5: 1640995200000,
		},
		epochBoundaryBlocks: make(map[uint64]uint64),
	}

	// Advance to epoch 6
	newTimestamp := uint64(time.Now().UnixMilli())
	err := cs.AdvanceEpoch(6, newTimestamp)
	require.NoError(t, err)

	// Verify state after advancement
	assert.Equal(t, uint64(6), cs.currentEpoch)
	assert.Equal(t, newTimestamp, cs.epochStartTimestampMs)

	// Verify historical timestamps
	// Epoch 5 (previous) should be stored in map with its original timestamp
	assert.Contains(t, cs.epochStartTimestamps, uint64(5))
	assert.Equal(t, uint64(1640995200000), cs.epochStartTimestamps[5])

	// Epoch 6 (current) timestamp is stored in epochStartTimestampMs, not in map
	// The map only contains historical epochs, current epoch timestamp is separate
}

// TestInitializeGenesisEpoch tests genesis epoch initialization
func TestInitializeGenesisEpoch(t *testing.T) {
	cs := &ChainState{
		currentEpoch:          0, // Should be reset
		epochStartTimestampMs: 0, // Should be reset
		epochStartTimestamps:  make(map[uint64]uint64),
		epochBoundaryBlocks:   make(map[uint64]uint64),
	}

	genesisTimestamp := uint64(1640995200000)
	cs.InitializeGenesisEpoch(genesisTimestamp)

	assert.Equal(t, uint64(0), cs.currentEpoch)
	assert.Equal(t, genesisTimestamp, cs.epochStartTimestampMs)
	assert.Contains(t, cs.epochStartTimestamps, uint64(0))
	assert.Equal(t, genesisTimestamp, cs.epochStartTimestamps[0])
}

// TestEpochDataBackupFilePersistence tests persistence using backup file
func TestEpochDataBackupFilePersistence(t *testing.T) {
	// Clean up any existing backup file
	backupFile := "/tmp/epoch_data_backup.json"
	os.Remove(backupFile)
	defer os.Remove(backupFile)

	// Create ChainState without storage manager (will use backup file)
	cs := &ChainState{
		storageManager:        nil, // Force use backup file
		currentEpoch:          3,
		epochStartTimestampMs: 1640995200000,
		epochStartTimestamps: map[uint64]uint64{
			0: 1640995200000 - 86400000*3,
			1: 1640995200000 - 86400000*2,
			2: 1640995200000 - 86400000*1,
			3: 1640995200000,
		},
	}

	// Test SaveEpochData (should save to backup file)
	err := cs.SaveEpochData()
	require.NoError(t, err, "SaveEpochData should not fail with backup file")

	// Verify backup file exists and contains correct data
	require.FileExists(t, backupFile, "Backup file should exist")

	fileData, err := os.ReadFile(backupFile)
	require.NoError(t, err, "Should be able to read backup file")

	var epochData EpochData
	err = json.Unmarshal(fileData, &epochData)
	require.NoError(t, err, "Should be able to unmarshal data from backup file")

	assert.Equal(t, cs.currentEpoch, epochData.CurrentEpoch)
	assert.Equal(t, cs.epochStartTimestampMs, epochData.EpochStartTimestampMs)
	assert.Equal(t, len(cs.epochStartTimestamps), len(epochData.EpochStartTimestamps))

	for epoch, timestamp := range cs.epochStartTimestamps {
		assert.Equal(t, timestamp, epochData.EpochStartTimestamps[epoch])
	}

	// Create new ChainState to test loading
	cs2 := &ChainState{
		storageManager:        nil, // Force use backup file
		currentEpoch:          0,
		epochStartTimestampMs: 0,
		epochStartTimestamps:  make(map[uint64]uint64),
	}

	// Test LoadEpochData (should load from backup file)
	err = cs2.LoadEpochData()
	require.NoError(t, err, "LoadEpochData should not fail with backup file")

	// Verify loaded data matches saved data
	assert.Equal(t, cs.currentEpoch, cs2.currentEpoch)
	assert.Equal(t, cs.epochStartTimestampMs, cs2.epochStartTimestampMs)
	assert.Equal(t, len(cs.epochStartTimestamps), len(cs2.epochStartTimestamps))

	for epoch, timestamp := range cs.epochStartTimestamps {
		assert.Equal(t, timestamp, cs2.epochStartTimestamps[epoch])
	}
}

// TestForceSaveEpochData tests forcing save via GetCurrentEpoch
func TestForceSaveEpochData(t *testing.T) {
	// Clean up any existing backup file
	backupFile := "/tmp/epoch_data_backup.json"
	os.Remove(backupFile)
	defer os.Remove(backupFile)

	// Create ChainState with epoch data
	cs := &ChainState{
		storageManager:        nil, // Force use backup file
		currentEpoch:          5,
		epochStartTimestampMs: 1640995200000,
		epochStartTimestamps: map[uint64]uint64{
			0: 1640995200000 - 86400000*5,
			1: 1640995200000 - 86400000*4,
			2: 1640995200000 - 86400000*3,
			3: 1640995200000 - 86400000*2,
			4: 1640995200000 - 86400000*1,
			5: 1640995200000,
		},
	}

	// Simulate GetCurrentEpoch handler (force save)
	if cs.GetCurrentEpoch() > 0 {
		err := cs.SaveEpochData()
		require.NoError(t, err, "Force save should succeed")
	}

	// Verify backup file was created
	require.FileExists(t, backupFile, "Backup file should exist after force save")

	// Verify content
	fileData, err := os.ReadFile(backupFile)
	require.NoError(t, err)

	var epochData EpochData
	err = json.Unmarshal(fileData, &epochData)
	require.NoError(t, err)

	assert.Equal(t, uint64(5), epochData.CurrentEpoch)
	assert.Equal(t, uint64(1640995200000), epochData.EpochStartTimestampMs)
	assert.Len(t, epochData.EpochStartTimestamps, 6)
}
