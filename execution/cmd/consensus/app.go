package main

import (
	"fmt"
	"math/rand"
	"time"
)

// Node đại diện cho một nút trong hệ thống phân tán
type Node struct {
	id    int
	value int
}

// Hàm cập nhật giá trị mới cho nút
func (n *Node) update(newValue int) {
	n.value = newValue
}

// Hàm simulateConsensus mô phỏng quá trình đồng thuận giữa các nút.
// Trong mỗi vòng, ta thu thập giá trị của tất cả các nút, kiểm tra nếu đã đồng thuận,
// nếu chưa thì cập nhật giá trị của mỗi nút thành giá trị lớn nhất thu được.
func simulateConsensus(nodes []*Node) int {
	round := 0
	for {
		round++
		// Thu thập giá trị của tất cả các nút trong vòng này
		var roundValues []int
		for _, node := range nodes {
			roundValues = append(roundValues, node.value)
		}

		// In ra giá trị của từng nút trong vòng hiện tại
		fmt.Printf("Vòng %d: %v\n", round, roundValues)

		// Kiểm tra đồng thuận: nếu tất cả các nút có cùng một giá trị
		consensus := true
		for i := 1; i < len(roundValues); i++ {
			if roundValues[i] != roundValues[0] {
				consensus = false
				break
			}
		}
		if consensus {
			return roundValues[0] // Trả về giá trị đồng thuận
		}

		// Tìm giá trị lớn nhất trong các giá trị của vòng này
		maxValue := roundValues[0]
		for _, v := range roundValues {
			if v > maxValue {
				maxValue = v
			}
		}

		// Cập nhật lại giá trị cho tất cả các nút theo quy tắc đồng thuận (chọn giá trị lớn nhất)
		for _, node := range nodes {
			node.update(maxValue)
		}

		// Nếu vượt quá số vòng cho phép (để tránh vòng lặp vô hạn) thì dừng
		if round > 10 {
			fmt.Println("Không đạt được đồng thuận sau 10 vòng")
			break
		}
	}
	return -1 // Trả về -1 nếu không đạt được đồng thuận
}

func main() {
	rand.Seed(time.Now().UnixNano()) // Khởi tạo seed cho hàm rand
	numIterations := 1000
	for i := 0; i < numIterations; i++ {
		// Khởi tạo danh sách các nút với giá trị ban đầu ngẫu nhiên
		nodes := []*Node{}
		for j := 1; j <= 5; j++ {
			nodes = append(nodes, &Node{id: j, value: rand.Intn(10)}) // Giá trị ngẫu nhiên từ 0 đến 9
		}
		fmt.Print("nodes state: [")
		for i, node := range nodes {
			fmt.Printf("{%d: %d}", node.id, node.value)
			if i < len(nodes)-1 {
				fmt.Print(", ")
			}
		}
		fmt.Println("]")
		// Gọi hàm đồng thuận
		result := simulateConsensus(nodes)
		if result != -1 {
			fmt.Printf("Lần chạy thứ %d: Đồng thuận đạt được với giá trị: %d\n", i+1, result)
		} else {
			fmt.Printf("Lần chạy thứ %d: Không đạt được đồng thuận\n", i+1)
		}
	}
}
