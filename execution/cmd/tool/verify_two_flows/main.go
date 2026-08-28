package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor"
)

const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[34m"
	colorCyan   = "\033[36m"
	colorBold   = "\033[1m"
)

func main() {
	fmt.Println()
	fmt.Println(colorCyan + colorBold + "════════════════════════════════════════════════════════════════════════════════════════" + colorReset)
	fmt.Println(colorCyan + colorBold + " 🔬 METANODE CORE: THỬ NGHIỆM ĐỘC LẬP 2 TÍNH NĂNG MỚI TRÊN MÁY" + colorReset)
	fmt.Println(colorCyan + colorBold + "    1. RegisterChainViaStake (Đăng ký chain bằng cọc, không cần vote)" + colorReset)
	fmt.Println(colorCyan + colorBold + "    2. Luồng chuyển tiền / gọi contract 2 chặng qua Reserve (A -> Reserve -> B)" + colorReset)
	fmt.Println(colorCyan + colorBold + "════════════════════════════════════════════════════════════════════════════════════════" + colorReset)
	fmt.Println()

	startTime := time.Now()
	results := tx_processor.RunTwoNewFlowsExperiment()
	totalElapsed := time.Since(startTime)

	currentFlow := ""
	allPassed := true
	passCount := 0
	failCount := 0

	for i, res := range results {
		if res.FlowName != currentFlow {
			currentFlow = res.FlowName
			fmt.Println()
			fmt.Printf("%s%s📦 [%s]%s\n", colorYellow, colorBold, currentFlow, colorReset)
			fmt.Println(strings.Repeat("─", 88))
		}

		statusTag := ""
		if res.Passed {
			statusTag = fmt.Sprintf("%s%s[PASS]%s", colorGreen, colorBold, colorReset)
			passCount++
		} else {
			statusTag = fmt.Sprintf("%s%s[FAIL]%s", colorRed, colorBold, colorReset)
			allPassed = false
			failCount++
		}

		fmt.Printf(" %02d. %-75s %s\n", i+1, res.Scenario, statusTag)
		fmt.Printf("     %s↳ Chi tiết:%s %s (%d ms)\n", colorCyan, colorReset, res.Details, res.DurationMs)
	}

	fmt.Println()
	fmt.Println(colorCyan + colorBold + "════════════════════════════════════════════════════════════════════════════════════════" + colorReset)
	fmt.Printf(colorBold+" 📊 KẾT QUẢ TỔNG QUAN: "+colorReset+"Tổng: %d kịch bản | %sĐạt: %d%s | %sThất bại: %d%s | Thời gian: %v\n",
		len(results),
		colorGreen, passCount, colorReset,
		colorRed, failCount, colorReset,
		totalElapsed.Round(time.Millisecond),
	)

	if allPassed {
		fmt.Println(colorGreen + colorBold + " 🎉 KẾT LUẬN: CẢ 2 LUỒNG MỚI ĐẠT 100% TIÊU CHUẨN, AN TOÀN, SẴN SÀNG TRIỂN KHAI TESTNET!" + colorReset)
		fmt.Println(colorCyan + colorBold + "════════════════════════════════════════════════════════════════════════════════════════" + colorReset)
		fmt.Println()
		os.Exit(0)
	} else {
		fmt.Println(colorRed + colorBold + " ❌ KẾT LUẬN: CÓ KỊCH BẢN THẤT BẠI - CẦN KIỂM TRA LẠI!" + colorReset)
		fmt.Println(colorCyan + colorBold + "════════════════════════════════════════════════════════════════════════════════════════" + colorReset)
		fmt.Println()
		os.Exit(1)
	}
}
