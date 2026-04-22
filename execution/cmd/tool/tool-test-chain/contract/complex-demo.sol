// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

contract ComplexDemo {
    struct Profile {
        string name;
        uint256 age;
    }

    Profile public userProfile;
    uint256[] public scores;

    event ProfileUpdated(string name, uint256 age);

    function updateProfile(Profile memory _profile) public {
        userProfile = _profile;
        emit ProfileUpdated(_profile.name, _profile.age);
    }

    function addScores(uint256[] memory _newScores) public {
        for (uint i = 0; i < _newScores.length; i++) {
            scores.push(_newScores[i]);
        }
    }

    function getProfile() public view returns (Profile memory) {
        return userProfile;
    }

    function getProfileAndScores() public view returns (Profile memory p, uint256[] memory s) {
        p = userProfile;
        s = scores;
    }
}
