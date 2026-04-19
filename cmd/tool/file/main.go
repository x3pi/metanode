package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"time"
)

type chunk struct {
	index  int64
	offset int64
	data   []byte
}

type receiverStats struct {
	totalChunks int64
	totalBytes  int64
	start       time.Time
	lastReport  time.Time
	lastChunks  int64
	lastBytes   int64
}

func main() {
	var (
		inputPath      string
		outputPath     string
		chunkSizeBytes int
		parallelism    int
		reportInterval time.Duration
	)

	flag.StringVar(&inputPath, "input", "", "đường dẫn file nguồn cần gửi")
	flag.StringVar(&outputPath, "output", "", "đường dẫn file đích; bỏ trống để bỏ qua việc ghi file nhận")
	flag.IntVar(&chunkSizeBytes, "chunk-size", 1*1024*1024, "kích thước chunk (bytes)")
	flag.IntVar(&parallelism, "parallel", 4, "số luồng gửi song song")
	flag.DurationVar(&reportInterval, "report-interval", 0, "khoảng thời gian báo cáo tạm thời (ví dụ 1s); 0 để chỉ in tổng kết")
	flag.Parse()

	if inputPath == "" {
		log.Fatal("bắt buộc phải cung cấp đường dẫn file nguồn thông qua cờ -input")
	}
	if chunkSizeBytes <= 0 {
		log.Fatal("kích thước chunk phải lớn hơn 0")
	}
	if parallelism <= 0 {
		log.Fatal("số luồng song song phải lớn hơn 0")
	}

	inputFile, err := os.Open(inputPath)
	if err != nil {
		log.Fatalf("không mở được file nguồn: %v", err)
	}
	defer inputFile.Close()

	inputInfo, err := inputFile.Stat()
	if err != nil {
		log.Fatalf("không lấy được thông tin file nguồn: %v", err)
	}

	var outputFile *os.File
	if outputPath != "" {
		outputFile, err = os.Create(outputPath)
		if err != nil {
			log.Fatalf("không tạo được file đích: %v", err)
		}
		defer outputFile.Close()
	}

	chunkCh := make(chan *chunk, parallelism*2)
	transferCh := make(chan *chunk, parallelism*2)

	var senders sync.WaitGroup
	for i := 0; i < parallelism; i++ {
		senders.Add(1)
		go func(id int) {
			defer senders.Done()
			for c := range chunkCh {
				payload := make([]byte, len(c.data))
				copy(payload, c.data)
				transferCh <- &chunk{
					index:  c.index,
					offset: c.offset,
					data:   payload,
				}
			}
		}(i)
	}

	var receiverWg sync.WaitGroup
	var receiverErr error
	stats := &receiverStats{}

	receiverWg.Add(1)
	go func() {
		defer receiverWg.Done()
		stats.start = time.Now()
		stats.lastReport = stats.start

		var ticker *time.Ticker
		if reportInterval > 0 {
			ticker = time.NewTicker(reportInterval)
			defer ticker.Stop()
		}

		for {
			if ticker != nil {
				select {
				case <-ticker.C:
					printInterimStats(stats, reportInterval)
					continue
				case c, ok := <-transferCh:
					if !ok {
						printFinalStats(stats)
						return
					}
					if err := handleChunk(outputFile, c, stats); err != nil {
						receiverErr = err
						return
					}
				}
			} else {
				c, ok := <-transferCh
				if !ok {
					printFinalStats(stats)
					return
				}
				if err := handleChunk(outputFile, c, stats); err != nil {
					receiverErr = err
					return
				}
			}
		}
	}()

	reader := bufio.NewReader(inputFile)
	startRead := time.Now()
	var index int64
	var offset int64
	for {
		buf := make([]byte, chunkSizeBytes)
		n, err := io.ReadFull(reader, buf)
		if err != nil {
			if err == io.EOF {
				break
			}
			if err == io.ErrUnexpectedEOF {
				if n > 0 {
					chunkCh <- &chunk{index: index, offset: offset, data: buf[:n]}
					index++
					offset += int64(n)
				}
				break
			}
			log.Fatalf("lỗi khi đọc file nguồn: %v", err)
		}
		chunkCh <- &chunk{index: index, offset: offset, data: buf[:n]}
		index++
		offset += int64(n)
	}
	close(chunkCh)

	senders.Wait()
	close(transferCh)
	receiverWg.Wait()

	if receiverErr != nil {
		log.Fatalf("lỗi khi xử lý phía nhận: %v", receiverErr)
	}

	elapsed := time.Since(startRead)
	fmt.Printf("\n=== Tóm tắt đọc/đẩy ===\n")
	fmt.Printf("File nguồn: %s (%.2f MB)\n", inputPath, float64(inputInfo.Size())/1024.0/1024.0)
	fmt.Printf("Chunk size: %d bytes | Luồng song song: %d\n", chunkSizeBytes, parallelism)
	fmt.Printf("Tổng chunks đọc: %d | Thời gian đọc: %s | Tốc độ đọc: %.2f chunk/s\n",
		index, elapsed, float64(index)/elapsed.Seconds())
}

func handleChunk(output *os.File, c *chunk, stats *receiverStats) error {
	if output != nil {
		if _, err := output.WriteAt(c.data, c.offset); err != nil {
			return fmt.Errorf("ghi chunk %d thất bại: %w", c.index, err)
		}
	}
	stats.totalChunks++
	stats.totalBytes += int64(len(c.data))
	return nil
}

func printInterimStats(stats *receiverStats, interval time.Duration) {
	now := time.Now()
	deltaChunks := stats.totalChunks - stats.lastChunks
	deltaBytes := stats.totalBytes - stats.lastBytes
	elapsed := now.Sub(stats.lastReport)
	if elapsed <= 0 {
		return
	}

	chunksPerSec := float64(deltaChunks) / elapsed.Seconds()
	mbPerSec := float64(deltaBytes) / 1024.0 / 1024.0 / elapsed.Seconds()

	fmt.Printf("[+] %s: %.2f chunk/s | %.2f MB/s (tổng %d chunks, %.2f MB)\n",
		now.Format(time.RFC3339), chunksPerSec, mbPerSec,
		stats.totalChunks, float64(stats.totalBytes)/1024.0/1024.0)

	stats.lastChunks = stats.totalChunks
	stats.lastBytes = stats.totalBytes
	stats.lastReport = now
}

func printFinalStats(stats *receiverStats) {
	totalElapsed := time.Since(stats.start)
	if stats.totalChunks == 0 || totalElapsed <= 0 {
		fmt.Println("Không có chunk nào được nhận.")
		return
	}
	chunksPerSec := float64(stats.totalChunks) / totalElapsed.Seconds()
	mbPerSec := float64(stats.totalBytes) / 1024.0 / 1024.0 / totalElapsed.Seconds()

	fmt.Printf("\n=== Tóm tắt nhận ===\n")
	fmt.Printf("Tổng chunks nhận: %d | Tổng dung lượng: %.2f MB\n",
		stats.totalChunks, float64(stats.totalBytes)/1024.0/1024.0)
	fmt.Printf("Thời gian nhận: %s | Tốc độ trung bình: %.2f chunk/s | %.2f MB/s\n",
		totalElapsed, chunksPerSec, mbPerSec)
}
