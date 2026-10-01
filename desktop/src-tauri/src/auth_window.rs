use std::time::{Duration, Instant, SystemTime};

const REUSE_WINDOW: Duration = Duration::from_secs(120);

/// Process-local approval. Reuse never extends the original verification time.
#[derive(Default)]
pub struct AuthWindow {
    epoch: u64,
    verified: Option<(Instant, SystemTime)>,
}

impl AuthWindow {
    pub fn begin(&mut self, now: Instant, wall: SystemTime) -> Option<u64> {
        if self.verified.is_some_and(|(mono, real)| {
            now.checked_duration_since(mono)
                .is_some_and(|age| age < REUSE_WINDOW)
                && wall
                    .duration_since(real)
                    .is_ok_and(|age| age < REUSE_WINDOW)
        }) {
            return None;
        }
        self.verified = None;
        Some(self.epoch)
    }

    pub fn complete(&mut self, epoch: u64, success: bool, now: Instant, wall: SystemTime) -> bool {
        if epoch != self.epoch {
            return false;
        }
        self.verified = success.then_some((now, wall));
        true
    }

    pub fn clear(&mut self) {
        self.epoch = self.epoch.wrapping_add(1);
        self.verified = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn reuses_success_for_two_minutes_without_sliding_expiry() {
        let mut window = AuthWindow::default();
        let (now, wall) = (Instant::now(), SystemTime::now());
        let epoch = window.begin(now, wall).unwrap();
        assert!(window.complete(epoch, true, now, wall));
        for seconds in [1, 60, 119] {
            let age = Duration::from_secs(seconds);
            assert_eq!(window.begin(now + age, wall + age), None);
        }
        assert!(window
            .begin(now + REUSE_WINDOW, wall + REUSE_WINDOW)
            .is_some());
    }

    #[test]
    fn failure_and_lock_do_not_grant_reuse_even_with_inflight_prompt() {
        let mut window = AuthWindow::default();
        let (now, wall) = (Instant::now(), SystemTime::now());
        let epoch = window.begin(now, wall).unwrap();
        window.complete(epoch, false, now, wall);
        assert!(window.begin(now, wall).is_some());
        window.clear();
        assert!(!window.complete(epoch, true, now, wall));
        let fresh = window.begin(now, wall).unwrap();
        window.complete(fresh, true, now, wall);
        window.clear();
        assert!(window.begin(now, wall).is_some());
    }

    #[test]
    fn sleep_and_backward_clock_changes_require_verification() {
        for wall_delta in [121i64, -1] {
            let mut window = AuthWindow::default();
            let (now, wall) = (Instant::now(), SystemTime::now());
            let epoch = window.begin(now, wall).unwrap();
            window.complete(epoch, true, now, wall);
            let later = if wall_delta > 0 {
                wall + Duration::from_secs(wall_delta as u64)
            } else {
                wall - Duration::from_secs(1)
            };
            assert!(window.begin(now + Duration::from_secs(1), later).is_some());
        }
    }
}
