use std::sync::Arc;

use failure::Fallible;
use io_context::Context;

use crate::{
    common::{crypto::hash::Hash, roothash::Namespace},
    storage::mkvs::{
        urkel::{cache::*, tree::*},
        LogEntry, WriteLog,
    },
};

impl UrkelTree {
    /// Commit tree updates to the underlying database and return
    /// the write log and new merkle root.
    pub fn commit(
        &mut self,
        ctx: Context,
        namespace: Namespace,
        round: u64,
    ) -> Fallible<(WriteLog, Hash)> {
        let ctx = ctx.freeze();
        let mut update_list: UpdateList<LRUCache> = UpdateList::new();
        let pending_root = self.cache.borrow().get_pending_root();
        let new_hash = _commit(&ctx, pending_root.clone(), &mut update_list, Some(round))?;

        update_list.commit(&mut self.cache.borrow_mut());

        let mut log: WriteLog = Vec::new();
        for (_, entry) in self.pending_write_log.iter() {
            // Skip all entries that do not exist after all the updates and
            // did not exist before.
            if entry.value.is_none() && !entry.existed {
                continue;
            }
            log.push(LogEntry {
                key: entry.key.clone(),
                value: entry.value.clone().unwrap_or_default(),
            });
        }
        self.pending_write_log.clear();
        self.cache.borrow_mut().set_sync_root(Root {
            namespace,
            round,
            hash: new_hash,
        });

        Ok((log, new_hash))
    }
}

pub fn _commit<C: Cache>(
    ctx: &Arc<Context>,
    ptr: NodePtrRef,
    update_list: &mut UpdateList<C>,
    round: Option<u64>,
) -> Fallible<Hash> {
    if ptr.borrow().clean {
        return Ok(ptr.borrow().hash);
    }

    match classify_noderef!(? ptr.borrow().node) {
        NodeKind::None => {
            ptr.borrow_mut().hash = Hash::empty_hash();
        }
        NodeKind::Internal => {
            let some_node_ref = ptr.borrow().get_node();
            if some_node_ref.borrow().is_clean() {
                ptr.borrow_mut().hash = some_node_ref.borrow().get_hash();
            } else {
                let int_leaf_node = noderef_as!(some_node_ref, Internal).leaf_node.clone();
                let int_left = noderef_as!(some_node_ref, Internal).left.clone();
                let int_right = noderef_as!(some_node_ref, Internal).right.clone();

                _commit(ctx, int_leaf_node.clone(), update_list, round)?;
                _commit(ctx, int_left.clone(), update_list, round)?;
                _commit(ctx, int_right.clone(), update_list, round)?;

                if let Some(round) = round {
                    noderef_as_mut!(some_node_ref, Internal).round = round;
                }
                some_node_ref.borrow_mut().update_hash();
                ptr.borrow_mut().hash = some_node_ref.borrow().get_hash();

                let closure_node_ref = some_node_ref.clone();
                update_list.push(Box::new(move |_| {
                    noderef_as_mut!(closure_node_ref, Internal).clean = true
                }));
            }
        }
        NodeKind::Leaf => {
            let node_ref = ptr.borrow().get_node();
            if node_ref.borrow().is_clean() {
                ptr.borrow_mut().hash = node_ref.borrow().get_hash();
            } else {
                if let Some(round) = round {
                    noderef_as_mut!(node_ref, Leaf).round = round;
                }
                node_ref.borrow_mut().update_hash();
                ptr.borrow_mut().hash = node_ref.borrow().get_hash();

                let closure_node_ref = node_ref.clone();
                update_list.push(Box::new(move |_| {
                    noderef_as_mut!(closure_node_ref, Leaf).clean = true
                }));
            }
        }
    };

    let closure_ptr = ptr.clone();
    update_list.push(Box::new(move |cache| {
        closure_ptr.borrow_mut().clean = true;
        // Make node eligible for eviction.
        cache.commit_node(closure_ptr.clone());
    }));

    Ok(ptr.borrow().hash)
}
