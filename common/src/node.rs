//! Node interface.
use std::convert::TryFrom;
#[cfg(not(target_env = "sgx"))]
use std::sync::Arc;

#[cfg(not(target_env = "sgx"))]
use grpcio;

use ekiden_common_api as api;

use super::address::Address;
use super::bytes::B256;
use super::error::{Error, Result};
#[allow(unused_imports)]
use super::x509::{Certificate, CERTIFICATE_COMMON_NAME};

/// Node.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Node {
    /// A public key identifying the node.
    pub id: B256,
    /// The public key identifying the `Entity` controlling the node.
    pub entity_id: B256,
    /// The epoch in which this nodes committment expires.
    pub expiration: u64,
    /// The list of `Address`es at which the node can be reached.
    pub addresses: Vec<Address>,
    /// Certificate for establishing TLS connections.
    pub certificate: Certificate,
    //TODO: define the reference to a stake.
    pub stake: Vec<u8>,
}

impl TryFrom<api::Node> for Node {
    type Error = Error;

    /// Convert a protobuf `common::api::Node` into a node.
    fn try_from(mut node: api::Node) -> Result<Self> {
        let mut addresses = node.take_addresses().into_vec();
        let addresses: Result<_> = addresses
            .drain(..)
            .map(|address| Address::try_from(address))
            .collect();
        let addresses = addresses?;

        Ok(Node {
            id: B256::try_from(node.get_id())?,
            entity_id: B256::try_from(node.get_entity_id())?,
            expiration: node.expiration,
            addresses: addresses,
            certificate: Certificate::try_from(node.get_certificate().clone())?,
            stake: node.get_stake().to_vec(),
        })
    }
}

impl Into<api::Node> for Node {
    /// Convert a node into a protobuf `common::api::Node` representation.
    fn into(mut self) -> api::Node {
        let mut node = api::Node::new();
        node.set_id(self.id.to_vec());
        node.set_entity_id(self.entity_id.to_vec());
        node.set_expiration(self.expiration);
        node.set_addresses(
            self.addresses
                .drain(..)
                .map(|address| address.into())
                .collect(),
        );
        node.set_certificate(self.certificate.into());
        node.set_stake(self.stake.clone());
        node
    }
}

#[cfg(not(target_env = "sgx"))]
impl Node {
    /// Construct a channel to given compute node.
    pub fn connect(&self, environment: Arc<grpcio::Environment>) -> grpcio::Channel {
        grpcio::ChannelBuilder::new(environment.clone())
            .override_ssl_target(CERTIFICATE_COMMON_NAME)
            .secure_connect(
                // TODO: Configure all addresses instead of just the first one.
                &format!("{}", self.addresses[0]),
                grpcio::ChannelCredentialsBuilder::new()
                    .root_cert(self.certificate.get_pem().unwrap())
                    .build(),
            )
    }
}

#[cfg(test)]
mod test {
    use super::*;

    #[test]
    fn test_node_conversion() {
        // Default node.
        let original = Node::default();
        let intermediate: api::Node = original.clone().into();
        let converted = Node::try_from(intermediate).unwrap();
        assert_eq!(original, converted);

        // Non-default node with some data.
        let mut original = Node::default();
        original.id = B256::random();
        original.entity_id = B256::random();
        original.expiration = 1_000_000_000;
        original.addresses = Address::for_local_port(42).unwrap();
        original.certificate = Certificate::generate().unwrap().0;
        original.stake = vec![42; 10];

        let intermediate: api::Node = original.clone().into();
        let converted = Node::try_from(intermediate).unwrap();
        assert_eq!(original, converted);
    }
}