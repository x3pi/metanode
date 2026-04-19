function haversine(lat1, lon1, lat2, lon2) {
    const R = 6371; // Bán kính Trái Đất (km)
    const toRad = angle => angle * (Math.PI / 180); // Chuyển đổi độ sang radian
  
    const dLat = toRad(lat2 - lat1);
    const dLon = toRad(lon2 - lon1);
  
    const a =
      Math.sin(dLat / 2) * Math.sin(dLat / 2) +
      Math.cos(toRad(lat1)) *
        Math.cos(toRad(lat2)) *
        Math.sin(dLon / 2) *
        Math.sin(dLon / 2);
  
    const c = 2 * Math.atan2(Math.sqrt(a), Math.sqrt(1 - a));
    return R * c; // Khoảng cách tính bằng km
  }
  
  // Ví dụ: Khoảng cách giữa Hà Nội và TP.HCM
  const distance = haversine(21, 105, 10, 106);
  console.log(`Khoảng cách: ${distance} km`);
  // kết quả evm: 1227.810874996405593378
  // kết quả js: 1227.8108749964053
  