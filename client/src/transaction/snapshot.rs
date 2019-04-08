//! A block snapshot.
use std::sync::Arc;

use ekiden_runtime::{
    common::{crypto::hash::Hash, roothash::Block},
    storage::{mkvs::CASPatriciaTrie, CAS, MKVS},
};
use failure::Fallible;

use super::{api, client::TxnClientError};

/// A partial block snapshot exposing the storage root.
pub struct BlockSnapshot {
    /// The (partial) block this snapshot is based on.
    pub block: Block,

    cas: Arc<CAS>,
    mkvs: CASPatriciaTrie,
}

impl Clone for BlockSnapshot {
    fn clone(&self) -> Self {
        let block = self.block.clone();
        let cas = self.cas.clone();
        let mkvs = CASPatriciaTrie::new(cas.clone(), &self.block.header.state_root);

        Self { block, cas, mkvs }
    }
}

impl BlockSnapshot {
    pub(super) fn new(storage_client: api::storage::StorageClient, block: Block) -> Self {
        let cas = Arc::new(RemoteCAS(storage_client));
        let mkvs = CASPatriciaTrie::new(cas.clone(), &block.header.state_root);

        Self { cas, mkvs, block }
    }
}

impl CAS for BlockSnapshot {
    fn get(&self, key: Hash) -> Fallible<Vec<u8>> {
        self.cas.get(key)
    }

    fn insert(&self, _value: Vec<u8>, _expiry: u64) -> Fallible<Hash> {
        unimplemented!("block snapshot is read-only");
    }
}

impl MKVS for BlockSnapshot {
    fn get(&self, key: &[u8]) -> Option<Vec<u8>> {
        self.mkvs.get(key)
    }

    fn insert(&mut self, _key: &[u8], _value: &[u8]) -> Option<Vec<u8>> {
        unimplemented!("block snapshot is read-only");
    }

    fn remove(&mut self, _key: &[u8]) -> Option<Vec<u8>> {
        unimplemented!("block snapshot is read-only");
    }

    fn commit(&mut self) -> Fallible<Hash> {
        unimplemented!("block snapshot is read-only");
    }

    fn rollback(&mut self) {
        unimplemented!("block snapshot is read-only");
    }

    fn set_encryption_key(&mut self, key: Option<&[u8]>) {
        self.mkvs.set_encryption_key(key)
    }
}

struct RemoteCAS(api::storage::StorageClient);

impl CAS for RemoteCAS {
    fn get(&self, key: Hash) -> Fallible<Vec<u8>> {
        // TODO: Tracing.

        let mut request = api::storage::GetRequest::new();
        request.set_id(key.as_ref().to_vec());

        let response = self
            .0
            .get(&request)
            .map_err(|error| TxnClientError::CallFailed(format!("{}", error)))?;

        Ok(response.data)
    }

    fn insert(&self, _value: Vec<u8>, _expiry: u64) -> Fallible<Hash> {
        unimplemented!("block snapshot is read-only");
    }
}