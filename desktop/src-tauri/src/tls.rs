use crate::protocol::Enrollment;
use rustls::{
    client::danger::{HandshakeSignatureValid, ServerCertVerified, ServerCertVerifier},
    client::WebPkiServerVerifier,
    pki_types::{pem::PemObject, CertificateDer, PrivateKeyDer, ServerName, UnixTime},
    CertificateError, ClientConfig, DigitallySignedStruct, Error, RootCertStore, SignatureScheme,
};
use sha2::{Digest, Sha256};
use std::sync::Arc;

#[derive(Debug)]
struct PinnedVerifier {
    // Standard chain, SAN, expiry and handshake signature verification is preserved.
    inner: Arc<WebPkiServerVerifier>,
    expected: [u8; 32],
}
impl ServerCertVerifier for PinnedVerifier {
    fn verify_server_cert(
        &self,
        cert: &CertificateDer<'_>,
        intermediates: &[CertificateDer<'_>],
        name: &ServerName<'_>,
        ocsp: &[u8],
        now: UnixTime,
    ) -> Result<ServerCertVerified, Error> {
        let actual: [u8; 32] = Sha256::digest(cert.as_ref()).into();
        if actual != self.expected {
            return Err(Error::InvalidCertificate(
                CertificateError::ApplicationVerificationFailure,
            ));
        }
        self.inner
            .verify_server_cert(cert, intermediates, name, ocsp, now)
    }
    fn verify_tls12_signature(
        &self,
        message: &[u8],
        cert: &CertificateDer<'_>,
        sig: &DigitallySignedStruct,
    ) -> Result<HandshakeSignatureValid, Error> {
        self.inner.verify_tls12_signature(message, cert, sig)
    }
    fn verify_tls13_signature(
        &self,
        message: &[u8],
        cert: &CertificateDer<'_>,
        sig: &DigitallySignedStruct,
    ) -> Result<HandshakeSignatureValid, Error> {
        self.inner.verify_tls13_signature(message, cert, sig)
    }
    fn supported_verify_schemes(&self) -> Vec<SignatureScheme> {
        self.inner.supported_verify_schemes()
    }
}
pub fn configuration(e: &Enrollment) -> Result<ClientConfig, String> {
    let expected: [u8; 32] = hex::decode(&e.server_certificate_sha256)
        .map_err(|_| "Invalid server pin")?
        .try_into()
        .map_err(|_| "Invalid server pin")?;
    let mut roots = RootCertStore::empty();
    for cert in CertificateDer::pem_slice_iter(e.server_ca_pem.as_bytes()) {
        roots
            .add(cert.map_err(|_| "Invalid server CA")?)
            .map_err(|_| "Invalid server CA")?;
    }
    if roots.is_empty() {
        return Err("Server CA is empty".into());
    }
    let certs = CertificateDer::pem_slice_iter(e.client_identity_pem.as_bytes())
        .collect::<Result<Vec<_>, _>>()
        .map_err(|_| "Invalid client certificate")?;
    let key = PrivateKeyDer::from_pem_slice(e.client_identity_pem.as_bytes())
        .map_err(|_| "Invalid client key")?;
    let provider = Arc::new(rustls::crypto::ring::default_provider());
    let inner = WebPkiServerVerifier::builder_with_provider(Arc::new(roots), provider.clone())
        .build()
        .map_err(|_| "Invalid server trust")?;
    ClientConfig::builder_with_provider(provider)
        .with_protocol_versions(&[&rustls::version::TLS13])
        .map_err(|_| "TLS 1.3 configuration failed")?
        .dangerous()
        .with_custom_certificate_verifier(Arc::new(PinnedVerifier { inner, expected }))
        .with_client_auth_cert(certs, key)
        .map_err(|_| "Client certificate and key do not match".into())
}
