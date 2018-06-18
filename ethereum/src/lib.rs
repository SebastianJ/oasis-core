// ! Ekiden Ethereum backend components.
extern crate chrono;
#[macro_use]
extern crate clap;
extern crate ekiden_beacon_base;
extern crate ekiden_common;
#[macro_use]
extern crate ekiden_di;
extern crate ekiden_epochtime;
extern crate ekiden_registry_base;
extern crate ekiden_registry_dummy;
extern crate ekiden_storage_base;
extern crate ethabi;
extern crate rustc_hex;
extern crate serde;
extern crate serde_json;
#[macro_use]
extern crate serde_derive;
extern crate serde_cbor;
extern crate tokio_core;
extern crate web3;
#[macro_use]
extern crate log;

mod beacon;
mod contract_registry;
mod entity_registry;
pub mod identity;
mod mockepoch;
pub mod truffle;
pub mod web3_di;

pub use beacon::EthereumRandomBeacon;
pub use contract_registry::EthereumContractRegistryBackend;
pub use entity_registry::EthereumEntityRegistryBackend;
pub use mockepoch::EthereumMockTime;