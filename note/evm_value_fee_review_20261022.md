# Review EVM / Block-STM: chuyển value sau khi trừ gas

## P1 — Contract nhận value nhưng sender không bị trừ value, receipt vẫn thành công

[ĐO] Đã tái hiện bằng giao dịch Ethereum Legacy có chữ ký hợp lệ, không bật bỏ qua signature verification. Cả hai giao dịch đều qua `VerifyTransaction` trên state trước block. Chạy luồng `ProcessTransactionsOptimistic → TrueBlockSTM → EVM` với MPT thật trong fixture của repository. [Log](evidence/evm_value_fee_review_20261022/production_pipeline.log), [test](evidence/evm_value_fee_review_20261022/review_test.go.txt).

[ĐO] Kịch bản:

1. Sender có 1,000,000,000,000 đơn vị nhỏ nhất; recipient có 1; leader có 0.
2. Tx nonce 1 chuyển 100,000,000,000 sang recipient, trả gas 2,000,000,000.
3. Tx nonce 2 deploy contract payable với value 898,000,000,000. Gas limit 100,000, gas price 100,000. Mỗi tx riêng lẻ đều đủ điều kiện số dư/phí trên state trước block.
4. Khi thực thi tx deploy, sender còn đúng bằng value. EVM chạy thành công; phí thực tế 5,320,100,000 được trừ trước khi áp dụng map chuyển tiền.
5. Contract được cộng 898,000,000,000. Sender vẫn còn 892,679,900,000, tức chỉ mất phí của deploy và không mất value. Receipt RETURNED.

[ĐO] Tổng số dư sender + recipient + contract + leader trước là **1,000,000,000,001**, sau là **1,898,000,000,001**. Assertion bảo toàn số dư FAIL. Đối chứng thêm đủ tiền vào sender thì tổng trước/sau đều **1,100,000,000,001**, test PASS. Đây là sai lệch tiền được commit, không chỉ sai log.

## Nguyên nhân

- [ĐỌC CODE] [true_block_stm.go:966](../execution/pkg/blockchain/tx_processor/true_block_stm.go#L966) trừ phí trước khi áp dụng thay đổi balance của EVM.
- [ĐỌC CODE] [true_block_stm.go:1001](../execution/pkg/blockchain/tx_processor/true_block_stm.go#L1001) cộng `MapAddBalance`, rồi [dòng 1012](../execution/pkg/blockchain/tx_processor/true_block_stm.go#L1012) gọi `SubTotalBalance` nhưng bỏ qua error. Khi trừ value thất bại, credit đã có trong MVCC và giao dịch vẫn thành công.
- [ĐỌC CODE] [mvcc_state_db.go:161](../execution/pkg/blockchain/tx_processor/mvcc/mvcc_state_db.go#L161) trả error khi không đủ số dư; không trừ tiền trong trường hợp đó.
- [ĐỌC CODE] Admission kiểm tra từng tx theo senderState trước execution; batch validation dùng cùng state của sender, không thể thay thế kiểm tra số dư tại thời điểm thực thi. [tx_validator_pool_core.go:680](../execution/cmd/simple_chain/processor/tx_validator_pool_core.go#L680).

## Hướng sửa

1. Đảm bảo phí và các delta balance của EVM được kiểm tra/applied một cách nguyên tử theo state của đúng version giao dịch. Nếu không đủ trả tổng nghĩa vụ, không được để lại credit, code/storage hoặc logs của phần thực thi bị từ chối.
2. Xử lý error của các thao tác MVCC thay vì bỏ qua. Phân biệt thiếu số dư với ErrEstimateHit để retry/suspend đúng khi cần. Chỉ thêm kiểm tra error sau khi đã credit là chưa đủ.
3. Giữ regression trên với chữ ký và admission hợp lệ; thêm test CALL có value, chuyển tiền nội bộ, revert và retry. Không chỉ kiểm tra deploy value=0.

## Phạm vi chứng minh

- [ĐO] Direct TrueBlockSTM cũng tái hiện, lưu ở direct_stm.log. Kết quả cuối cùng dùng production_pipeline.log với luồng optimistic và kiểm tra chữ ký bật.
- [CHƯA ĐO] Chưa gửi qua RPC/mempool trên cluster đang chạy, chưa chạy NOMT, chưa kiểm tra rút khoản credit khỏi contract. Không kết luận có fork/deadlock hoặc đã khai thác trên mạng thật.
- [ĐỌC CODE] Lỗi không cần Xapian. Kết quả đồng nhất giữa các node cũng không đảm bảo bảo toàn tổng số dư.
- Chỉ thêm tài liệu và evidence; không sửa source chạy.

## Chạy lại

Từ root metanode, chạy `python3 note/evidence/evm_value_fee_review_20261022/run.py "$PWD"`. Script dùng Go overlay, không thêm test vào package thật. Cần các thư viện native đã build trong workspace. `-vet=off` vì vet không mở được tên file ảo của overlay trên toolchain hiện tại; test vẫn được compile và thực thi. Exit 1 là assertion bảo toàn số dư thất bại như log, không phải test đã pass.

Các probe trước cũng được giữ lại: lần nonce 0 bị admission từ chối đúng quy định tx0; probe cuối dùng nonce 1/2 và qua admission. Các probe unsigned ban đầu không dùng làm bằng chứng xác nhận đường ký/admission. HEAD và hash source tại [metadata](evidence/evm_value_fee_review_20261022/metadata.txt).
