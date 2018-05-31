//! Utilities for testing consensus backends.
use std::sync::{Arc, Mutex};

use ekiden_common::bytes::B256;
use ekiden_common::error::Error;
use ekiden_common::futures::sync::{mpsc, oneshot};
use ekiden_common::futures::{future, BoxFuture, Future, FutureExt, Stream};
use ekiden_common::hash::empty_hash;
use ekiden_common::ring::signature::Ed25519KeyPair;
use ekiden_common::signature::InMemorySigner;
use ekiden_common::untrusted;
use ekiden_scheduler_base::{CommitteeType, Role, Scheduler};
use ekiden_storage_base::{hash_storage_key, StorageBackend};

use super::*;

/// Command sent to a simulated compute node.
pub enum Command {
    /// Computation has been received.
    Compute(Vec<u8>),
}

/// Simulated batch of contract invocations.
pub struct SimulatedComputationBatch {
    /// Block produced by the computation batch.
    block: Block,
    /// Nonce used for commitment.
    nonce: B256,
}

impl SimulatedComputationBatch {
    /// Create new simulated computation batch.
    pub fn new(child: Block, output: &[u8]) -> Self {
        let mut block = Block::new_parent_of(&child);
        // We currently just assume that the computation group is fixed.
        block.computation_group = child.computation_group;
        block.header.input_hash = empty_hash();
        block.header.output_hash = empty_hash();
        block.header.state_root = hash_storage_key(output);
        block.update();

        // TODO: Include some simulated transactions.

        Self {
            block,
            nonce: B256::random(),
        }
    }
}

struct SimulatedNodeInner {
    /// Contract identifier.
    contract_id: B256,
    /// Storage backend.
    storage: Arc<StorageBackend>,
    /// Signer for the simulated node.
    signer: InMemorySigner,
    /// Public key for the simulated node.
    public_key: B256,
    /// Shutdown channel.
    shutdown_channel: Option<oneshot::Sender<()>>,
    /// Command channel.
    command_channel: Option<mpsc::UnboundedSender<Command>>,
    /// Current simulated computation.
    computation: Option<SimulatedComputationBatch>,
}

/// A simulated node.
pub struct SimulatedNode {
    inner: Arc<Mutex<SimulatedNodeInner>>,
}

impl SimulatedNode {
    /// Create new simulated node.
    pub fn new(storage: Arc<StorageBackend>, contract_id: B256) -> Self {
        let key_pair =
            Ed25519KeyPair::from_seed_unchecked(untrusted::Input::from(&B256::random())).unwrap();
        let public_key = B256::from(key_pair.public_key_bytes());
        let signer = InMemorySigner::new(key_pair);

        Self {
            inner: Arc::new(Mutex::new(SimulatedNodeInner {
                contract_id,
                storage,
                signer,
                public_key,
                command_channel: None,
                computation: None,
                shutdown_channel: None,
            })),
        }
    }

    /// Get public key for this node.
    pub fn get_public_key(&self) -> B256 {
        let inner = self.inner.lock().unwrap();
        inner.public_key.clone()
    }

    /// Start simulated node.
    pub fn start(
        &self,
        backend: Arc<ConsensusBackend>,
        scheduler: Arc<Scheduler>,
    ) -> BoxFuture<()> {
        // Create a channel for external commands. This is currently required because
        // there is no service discovery backend which we could subscribe to.
        let (sender, receiver) = mpsc::unbounded();
        let (contract_id, public_key) = {
            let mut inner = self.inner.lock().unwrap();
            assert!(inner.command_channel.is_none());
            inner.command_channel.get_or_insert(sender);

            (inner.contract_id, inner.public_key)
        };

        // Fetch current committee to get our role.
        let role = scheduler
            .watch_committees()
            .filter(|committee| committee.kind == CommitteeType::Compute)
            .take(1)
            .collect()
            .wait()
            .unwrap()
            .first()
            .unwrap()
            .members
            .iter()
            .filter(|node| node.public_key == public_key)
            .map(|node| node.role)
            .next()
            .unwrap();

        // Subscribe to new events.
        let event_processor: BoxFuture<()> = {
            let shared_inner = self.inner.clone();
            let backend = backend.clone();

            Box::new(
                backend
                    .get_events(contract_id)
                    .for_each(move |event| -> BoxFuture<()> {
                        let mut inner = shared_inner.lock().unwrap();

                        match event {
                            Event::CommitmentsReceived(_) => {
                                // Generate reveal.
                                let computation = match inner.computation.take() {
                                    Some(computation) => computation,
                                    None => return future::ok(()).into_box(),
                                };

                                let reveal = Reveal::new(
                                    &inner.signer,
                                    &computation.nonce,
                                    &computation.block.header,
                                );

                                // Send reveal.
                                backend.reveal(inner.contract_id, reveal)
                            }
                            Event::RoundFailed(error) => {
                                // Round has failed, so the test should abort.
                                panic!("round failed: {}", error);
                            }
                            Event::DiscrepancyDetected(_) => {
                                // Discrepancy has been detected.
                                panic!("unexpected discrepancy detected during test");
                            }
                        }
                    }),
            )
        };

        // Process commands.
        let command_processor: BoxFuture<()> = {
            let shared_inner = self.inner.clone();
            let backend = backend.clone();
            let contract_id = contract_id.clone();

            Box::new(
                receiver
                    .map_err(|_| Error::new("command channel closed"))
                    .for_each(move |command| -> BoxFuture<()> {
                        match command {
                            Command::Compute(output) => {
                                if role == Role::BackupWorker {
                                    // TODO: Support testing discrepancy resolution.
                                    return future::ok(()).into_box();
                                }

                                // Fetch latest block.
                                let latest_block = backend.get_latest_block(contract_id);

                                let shared_inner = shared_inner.clone();
                                let backend = backend.clone();
                                let contract_id = contract_id.clone();

                                Box::new(latest_block.and_then(move |block| {
                                    let mut inner = shared_inner.lock().unwrap();

                                    // Start new computation with some dummy output state.
                                    let computation =
                                        SimulatedComputationBatch::new(block, &output);

                                    // Generate commitment.
                                    let commitment = Commitment::new(
                                        &inner.signer,
                                        &computation.nonce,
                                        &computation.block.header,
                                    );

                                    // Store computation.
                                    assert!(inner.computation.is_none());
                                    inner.computation.get_or_insert(computation);

                                    if !output.is_empty() {
                                        // Insert dummy result to storage and commit.
                                        inner
                                            .storage
                                            .insert(output, 7)
                                            .and_then(move |_| {
                                                backend.commit(contract_id, commitment)
                                            })
                                            .into_box()
                                    } else {
                                        // Output is empty, no need to insert to storage.
                                        backend.commit(contract_id, commitment).into_box()
                                    }
                                }))
                            }
                        }
                    })
                    .or_else(|error| {
                        panic!(
                            "error while processing simulated compute node command: {:?}",
                            error
                        );

                        #[allow(unreachable_code)]
                        Ok(())
                    }),
            )
        };

        // Create shutdown channel.
        let (sender, receiver) = oneshot::channel();
        {
            let mut inner = self.inner.lock().unwrap();
            assert!(inner.shutdown_channel.is_none());
            inner.shutdown_channel.get_or_insert(sender);
        }

        let shutdown = Box::new(receiver.then(|_| Err(Error::new("shutdown"))));

        let tasks =
            future::join_all(vec![event_processor, command_processor, shutdown]).then(|_| Ok(()));

        Box::new(tasks)
    }

    /// Simulate delivery of a new computation.
    pub fn compute(&self, output: &[u8]) {
        let inner = self.inner.lock().unwrap();
        let channel = inner.command_channel.as_ref().unwrap();
        channel
            .unbounded_send(Command::Compute(output.to_vec()))
            .unwrap();
    }

    /// Shutdown node.
    pub fn shutdown(&self) {
        let mut inner = self.inner.lock().unwrap();
        let channel = inner.shutdown_channel.take().unwrap();
        drop(channel.send(()));
    }
}

/// Generate the specified number of simulated compute nodes.
pub fn generate_simulated_nodes(
    count: usize,
    storage: Arc<StorageBackend>,
    contract_id: B256,
) -> Vec<SimulatedNode> {
    (0..count)
        .map(|_| SimulatedNode::new(storage.clone(), contract_id))
        .collect()
}
