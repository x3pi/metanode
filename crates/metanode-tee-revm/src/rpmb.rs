use alloc::string::String;
use revm_primitives::U256;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct RpmbData {
    pub monotonic_counter: u64,
    pub state_root: U256,
}

impl Default for RpmbData {
    fn default() -> Self {
        Self {
            monotonic_counter: 0,
            state_root: U256::ZERO,
        }
    }
}

pub trait RpmbProvider {
    fn read_data(&self) -> Result<RpmbData, String>;
    fn write_data(&self, data: RpmbData) -> Result<(), String>;
}

pub struct AntiReplayGuard<'a> {
    provider: &'a dyn RpmbProvider,
}

impl<'a> AntiReplayGuard<'a> {
    pub fn new(provider: &'a dyn RpmbProvider) -> Self {
        Self { provider }
    }

    /// Xác thực counter phải lớn hơn counter hiện tại trong RPMB
    /// Nếu hợp lệ, ghi đè state_root và counter mới vào RPMB.
    pub fn verify_and_commit(&self, new_counter: u64, new_state_root: U256) -> Result<(), String> {
        let current_data = self.provider.read_data()?;
        
        if new_counter <= current_data.monotonic_counter {
            return Err(alloc::format!(
                "🔥 ROLLBACK ATTACK DETECTED! Provided counter ({}) is not strictly greater than RPMB counter ({})",
                new_counter,
                current_data.monotonic_counter
            ));
        }

        let new_data = RpmbData {
            monotonic_counter: new_counter,
            state_root: new_state_root,
        };

        self.provider.write_data(new_data)?;
        Ok(())
    }
}
