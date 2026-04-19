// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

interface IBigMathV0_1 {
    function FromInt256String(int256 value) external pure returns (string memory);
    
    // Phép toán cơ bản
    function div(int256 a, int256 b) external pure returns (int256);
    function mul(int256 a, int256 b) external pure returns (int256);
    function sub(int256 a, int256 b) external pure returns (int256);
    function add(int256 a, int256 b) external pure returns (int256);
    function mod(int256 a, int256 b) external pure returns (int256);

    // Lượng giác
    function sin(int256 value) external pure returns (int256);
    function cos(int256 value) external pure returns (int256);
    function asin(int256 value) external pure returns (int256);
    function sqrt(int256 value) external pure returns (int256);
    function atan2(int256 value , int256 b) external pure returns (int256);

    // Số Pi
    function PI() external pure returns (int256);
}

contract HaversineDistance {

    IBigMathV0_1 public bigMath = IBigMathV0_1(0x0000000000000000000000000000000000000104);
    int256 private constant R = 6371000000000000000; // Bán kính Trái Đất (m), nhân 10^18
    int256 private constant SCALE = 10**18; // Hệ số nhân để tính toán chính xác

    function toRad(int256 degree) internal view returns (int256) {
        return bigMath.div(bigMath.mul(degree, bigMath.PI()), int256(180) * SCALE); 
    }

    function haversine(
        int256 lat1, int256 lon1, int256 lat2, int256 lon2
    ) external view returns (int256) {
        int256 dLat = toRad(bigMath.sub(lat2, lat1)); 

        int256 dLon = toRad(bigMath.sub(lon2, lon1)); 

        int256 lat1Rad = toRad(lat1); 
        int256 lat2Rad = toRad(lat2);

        int256 sinDLat = bigMath.sin(bigMath.div(dLat, 2* SCALE)); 
        int256 sinDLon = bigMath.sin(bigMath.div(dLon, 2* SCALE));

        int256 a = bigMath.add( 
            bigMath.mul(sinDLat, sinDLat),
            bigMath.mul(
                bigMath.mul(bigMath.cos(lat1Rad), bigMath.cos(lat2Rad)),
                bigMath.mul(sinDLon, sinDLon)
            )
        );
        
        int256 sqrtA = bigMath.sqrt(a);
        int256 sqrtOneMinusA = bigMath.sqrt(bigMath.sub(SCALE, a));
        int256 c = bigMath.mul(int256(2* SCALE), bigMath.atan2(sqrtA, sqrtOneMinusA));
        return bigMath.mul(R, c);
    }

    function toRadP(int256 degree) external view returns  (int256){
        return bigMath.div(bigMath.mul(degree, bigMath.PI()), int256(180) * SCALE); 

    }
}
