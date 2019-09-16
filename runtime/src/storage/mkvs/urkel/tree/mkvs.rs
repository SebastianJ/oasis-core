use failure::Fallible;
use io_context::Context;

use crate::{
    common::{crypto::hash::Hash, roothash::Namespace},
    storage::mkvs::{urkel::tree::*, Prefix, WriteLog, MKVS},
};

unsafe impl Send for UrkelTree {}
unsafe impl Sync for UrkelTree {}

// TODO: We should likely change the MKVS interface to propagate errors instead of unwrapping.

impl MKVS for UrkelTree {
    fn get(&mut self, ctx: Context, key: &[u8]) -> Option<Vec<u8>> {
        let _lock = self.lock.lock().unwrap();
        UrkelTree::get(self, ctx, key).unwrap()
    }

    fn insert(&mut self, ctx: Context, key: &[u8], value: &[u8]) -> Option<Vec<u8>> {
        let lock = self.lock.clone();
        let _guard = lock.lock().unwrap();
        self.insert(ctx, key, value).unwrap()
    }

    fn remove(&mut self, ctx: Context, key: &[u8]) -> Option<Vec<u8>> {
        let lock = self.lock.clone();
        let _guard = lock.lock().unwrap();
        self.remove(ctx, key).unwrap()
    }

    fn prefetch_prefixes(&self, ctx: Context, prefixes: &Vec<Prefix>, limit: u16) {
        let lock = self.lock.clone();
        let _guard = lock.lock().unwrap();
        self.prefetch_prefixes(ctx, prefixes, limit).unwrap()
    }

    fn commit(
        &mut self,
        ctx: Context,
        namespace: Namespace,
        round: u64,
    ) -> Fallible<(WriteLog, Hash)> {
        let lock = self.lock.clone();
        let _guard = lock.lock().unwrap();
        UrkelTree::commit(self, ctx, namespace, round)
    }

    fn rollback(&mut self) {
        let lock = self.lock.clone();
        let _guard = lock.lock().unwrap();
        self.pending_write_log.clear();
    }
}
