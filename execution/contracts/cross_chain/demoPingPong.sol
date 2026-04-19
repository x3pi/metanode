// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

interface ICrossChainGateway {
    function sendMessage(address target, bytes calldata payload, uint256 destinationId) external payable;
}

interface IMetaNodeContext {
    // get msg sender (Contract caller)
    function getOriginalSender() external view returns (address);
    // get source chain id
    function getSourceChainId() external view returns (uint256);
}

contract CrossChainPingPong {
    struct Ball {
        uint256 rallyCount;   // số lần đập qua lại
        uint256 lastChainId;  // chain nào đập lần cuối
        address lastHitter;   // ai đập lần cuối
    }

    ICrossChainGateway public immutable gateway;
    uint256 public immutable homeChainId; // Nation ID của chain này

    Ball public ball;
    bool public hasBall;

    IMetaNodeContext constant CROSS_CHAIN_CONTEXT = IMetaNodeContext(0x00000000000000000000000000000000B429C0B2);

    event BallSent(uint256 fromChain, uint256 toChain, Ball ball);
    event BallReceived(uint256 fromChain, uint256 toChain, Ball ball);

    constructor() {
        gateway = ICrossChainGateway(0x00000000000000000000000000000000B429C0B2);
        homeChainId = block.chainid;
    }

    /// @notice Phải được gọi lần đầu để tạo quả bóng trên chain này
    function serveBall() external {
        require(!hasBall, "Already have ball");
        ball = Ball({rallyCount: 0, lastChainId: homeChainId, lastHitter: msg.sender});
        hasBall = true;
    }

    /// @notice Đập bóng sang chain khác
    function hitBallTo(uint256 destChainId) external {
        require(hasBall, "No ball here!");
        Ball memory newBall = Ball({
            rallyCount: ball.rallyCount + 1,
            lastChainId: homeChainId,
            lastHitter: msg.sender
        });
        hasBall = false; // Bóng bay đi
        delete ball;

        // Gửi sang chain đích — payload = abi.encode(receiveBall, newBall)
        bytes memory payload = abi.encodeWithSignature(
            "receiveBall((uint256,uint256,address))",
            newBall
        );
        gateway.sendMessage(address(this), payload, destChainId);
        emit BallSent(homeChainId, destChainId, newBall);
    }

    /// @notice ⚡ Được Go Node gọi khi có cross-chain message đến
    function receiveBall(Ball memory _ball) external {
        address originalSender;
        uint256 sourceChainId;

        // 
        try CROSS_CHAIN_CONTEXT.getOriginalSender() returns (address sender) {
            originalSender = sender;
        } catch {
            revert("Not in cross-chain context");
        }

        require(originalSender == address(this), "Invalid cross-chain sender");
        
        try CROSS_CHAIN_CONTEXT.getSourceChainId() returns (uint256 srcChain) {
            sourceChainId = srcChain;
        } catch {
            revert("Not in cross-chain context");
        }

        // Nhận bóng
        ball = _ball;
        hasBall = true;

        emit BallReceived(sourceChainId, homeChainId, _ball);
    }
}

// =========================================================================
// HỢP ĐỒNG DEPLOYER: Dùng để Deploy PingPong bằng CREATE2
// =========================================================================
contract PingPongFactory {
    event Deployed(address addr, bytes32 salt);

    // Triển khai PingPong Contract bằng CREATE2
    // Vi cả gateway và chainid đều lấy tự động, không cần truyền tham số
    function deployPingPong(bytes32 salt) external returns (address) {
        // Encode bytecode ko có params
        bytes memory bytecode = type(CrossChainPingPong).creationCode;

        address addr;
        assembly {
            addr := create2(0, add(bytecode, 0x20), mload(bytecode), salt)
        }
        require(addr != address(0), "CREATE2 Deploy Failed");
        
        emit Deployed(addr, salt);
        return addr;
    }

    // Tiên đoán địa chỉ trước khi deploy
    function predictAddress(bytes32 salt) external view returns (address) {
        bytes memory bytecode = type(CrossChainPingPong).creationCode;
        bytes32 hash = keccak256(
            abi.encodePacked(
                bytes1(0xff),
                address(this),
                salt,
                keccak256(bytecode)
            )
        );
        return address(uint160(uint256(hash)));
    }
}
