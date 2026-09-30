//! Per-process credential copies; never serialized. Session authorization remains
//! mandatory at command entry, including after the five-minute app lock.
use std::collections::BTreeMap;
use zeroize::Zeroizing;

type Secret = Option<Zeroizing<Vec<u8>>>;
#[derive(Default)]
pub struct CredentialCache {
    entries: BTreeMap<(String, String), Secret>,
}
impl CredentialCache {
    pub fn read(&mut self, service: &str, account: &str, load: impl FnOnce() -> Result<Secret, String>) -> Result<Secret, String> {
        let key = (service.to_owned(), account.to_owned());
        if let Some(value) = self.entries.get(&key) { return Ok(value.clone()); }
        let value = load()?;
        self.entries.insert(key, value.clone());
        Ok(value)
    }
    pub fn set(&mut self, service: &str, account: &str, value: Secret) {
        self.entries.insert((service.to_owned(), account.to_owned()), value);
    }
    pub fn clear(&mut self) { self.entries.clear(); }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn reads_keychain_once_and_refreshes_saved_values() {
        let mut cache = CredentialCache::default();
        assert_eq!(&**cache.read("providers", "primary", || Ok(Some(Zeroizing::new(b"token".to_vec())))).unwrap().as_ref().unwrap(), b"token");
        cache.read("providers", "primary", || panic!("repeated Keychain read")).unwrap();
        cache.set("providers", "primary", Some(Zeroizing::new(b"replacement".to_vec())));
        assert_eq!(&**cache.read("providers", "primary", || panic!()).unwrap().as_ref().unwrap(), b"replacement");
        cache.set("providers", "primary", None);
        assert!(cache.read("providers", "primary", || panic!()).unwrap().is_none());
    }
    #[test]
    fn missing_is_cached_denied_is_retryable_and_accounts_are_isolated() {
        let mut cache = CredentialCache::default();
        cache.read("ssh", "host-a-pin-a", || Ok(None)).unwrap();
        cache.read("ssh", "host-a-pin-a", || panic!()).unwrap();
        assert!(cache.read("ssh", "host-a-pin-b", || Err("denied".into())).is_err());
        cache.read("ssh", "host-a-pin-b", || Ok(Some(Zeroizing::new(vec![1])))).unwrap();
        cache.clear();
        assert!(cache.entries.is_empty());
        cache.read("ssh", "host-a-pin-a", || Ok(None)).unwrap();
    }
}
