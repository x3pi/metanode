// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

interface IBigMathV0_1 {
    function FromInt256String(int256 value) external pure returns (string memory);
    function div(int256 a, int256 b) external pure returns (int256);
    function mul(int256 a, int256 b) external pure returns (int256);
    function sub(int256 a, int256 b) external pure returns (int256);
    function add(int256 a, int256 b) external pure returns (int256);
    function PI() external pure returns (int256);
    function asin(int256 value) external pure returns (int256);
    function acos(int256 value) external pure returns (int256);
    function atan(int256 value) external pure returns (int256);
    function atan2(int256 y, int256 x) external pure returns (int256);
    function sqrt(int256 value) external pure returns (int256);
    function root(int256 value, int256 n) external pure returns (int256);
    function sin(int256 value) external pure returns (int256);
    function cos(int256 value) external pure returns (int256);
    function tan(int256 value) external pure returns (int256);
    function cot(int256 value) external pure returns (int256);
    function sec(int256 value) external pure returns (int256);
    function csc(int256 value) external pure returns (int256);
    function sinh(int256 value) external pure returns (int256);
    function cosh(int256 value) external pure returns (int256);
    function tanh(int256 value) external pure returns (int256);
    function log(int256 value) external pure returns (int256);
    function log2(int256 value) external pure returns (int256);
    function log10(int256 value) external pure returns (int256);
    function exp(int256 value) external pure returns (int256);
    function exp2(int256 value) external pure returns (int256);
    function pow(int256 base, int256 exponent) external pure returns (int256);
    function mod(int256 base, int256 exponent) external pure returns (int256);
    function gcd(int256 base, int256 exponent) external pure returns (int256);
    function lcm(int256 base, int256 exponent) external pure returns (int256);

}

contract PublicMath {
    IBigMathV0_1 public BigMath = IBigMathV0_1(0x0000000000000000000000000000000000000104);

    function getPi() public view returns (int256) {
        return BigMath.PI();
    }

    function getString(int256 value) public view returns (string memory) {
        return BigMath.Fromint256String(value);
    }

    function testAdd(int256 a, int256 b) public view returns (int256) {
        return BigMath.add(a, b);
    }

    function testSub(int256 a, int256 b) public view returns (int256) {
        return BigMath.sub(a, b);
    }

    function testMul(int256 a, int256 b) public view returns (int256) {
        return BigMath.mul(a, b);
    }

    function testDiv(int256 a, int256 b) public view returns (int256) {
        require(b != 0, "Division by zero");
        return BigMath.div(a, b);
    }

    function testAsin(int256 value) public view returns (int256) {
        return BigMath.asin(value);
    }

    function testAcos(int256 value) public view returns (int256) {
        return BigMath.acos(value);
    }

    function testAtan(int256 value) public view returns (int256) {
        return BigMath.atan(value);
    }

    function testAtan2(int256 y, int256 x) public view returns (int256) {
        return BigMath.atan2(y, x);
    }

    function testSqrt(int256 value) public view returns (int256) {
        return BigMath.sqrt(value);
    }

    function testRoot(int256 value, int256 n) public view returns (int256) {
        return BigMath.root(value, n);
    }

    function testSin(int256 value) public view returns (int256) {
        return BigMath.sin(value);
    }

    function testCos(int256 value) public view returns (int256) {
        return BigMath.cos(value);
    }

    function testTan(int256 value) public view returns (int256) {
        return BigMath.tan(value);
    }

    function testCot(int256 value) public view returns (int256) {
        return BigMath.cot(value);
    }

    function testSec(int256 value) public view returns (int256) {
        return BigMath.sec(value);
    }

    function testCsc(int256 value) public view returns (int256) {
        return BigMath.csc(value);
    }

    function testSinh(int256 value) public view returns (int256) {
        return BigMath.sinh(value);
    }

    function testCosh(int256 value) public view returns (int256) {
        return BigMath.cosh(value);
    }

    function testTanh(int256 value) public view returns (int256) {
        return BigMath.tanh(value);
    }

    function testLog(int256 value) public view returns (int256) {
        return BigMath.log(value);
    }

    function testLog2(int256 value) public view returns (int256) {
        return BigMath.log2(value);
    }

    function testLog10(int256 value) public view returns (int256) {
        return BigMath.log10(value);
    }

    function testExp(int256 value) public view returns (int256) {
        return BigMath.exp(value);
    }

    function testExp2(int256 value) public view returns (int256) {
        return BigMath.exp2(value);
    }

    function testMod(int256 base, int256 exponent) public view returns (int256) {
        return BigMath.mod(base, exponent);
    }

    function testPow(int256 base, int256 exponent) public view returns (int256) {
        return BigMath.pow(base, exponent);
    }

    function testGcd(int256 base, int256 exponent) public view returns (int256) {
        return BigMath.gcd(base, exponent);
    }

    function testLcm(int256 base, int256 exponent) public view returns (int256) {
        return BigMath.lcm(base, exponent);
    }
}
