//! Storage frontend - a router to be used to access storage.
use std::sync::{Arc, Mutex};

use grpcio::Environment;

use ekiden_common::bytes::{B256, H256};
use ekiden_common::error::Error;
use ekiden_common::futures::{future, BoxFuture, Future};
use ekiden_common::node::Node;
use ekiden_registry_base::EntityRegistryBackend;
use ekiden_scheduler_base::{Committee, CommitteeType, Scheduler};
use ekiden_storage_base::StorageBackend;

use client::StorageClient;

struct StorageFrontendInner {
    /// Contract context for storage operations.
    contract_id: B256,
    /// Notification of committee changes.
    scheduler: Arc<Scheduler>,
    /// Registry of nodes.
    registry: Arc<EntityRegistryBackend>,
    /// gRPC environment for scheduling execution.
    environment: Arc<Environment>,
    /// How agressively to retry connections to backends before erroring.
    retries: usize,
}

/// StorageFrontend provides a storage interface routed to active storage backends for a given
/// `Contract`, as directed by the Ekiden `Scheduler`.
pub struct StorageFrontend {
    /// Active connections to storage backends.
    clients: Arc<Mutex<Vec<Arc<StorageClient>>>>,
    /// Shared state associated with the storage frontend.
    inner: Arc<StorageFrontendInner>,
}

impl StorageFrontend {
    /// Create a new frontend that uses clients based on pointers from the scheduler.
    pub fn new(
        contract_id: B256,
        scheduler: Arc<Scheduler>,
        registry: Arc<EntityRegistryBackend>,
        environment: Arc<Environment>,
        retries: usize,
    ) -> Self {
        Self {
            clients: Arc::new(Mutex::new(vec![])),
            inner: Arc::new(StorageFrontendInner {
                contract_id: contract_id,
                scheduler: scheduler.clone(),
                registry: registry.clone(),
                environment: environment.clone(),
                retries: retries,
            }),
        }
    }

    /// Refreshes the list of active storage connections by polling the scheduler for the active
    /// storage committee for a given contract.
    fn refresh(
        cid: B256,
        registry: Arc<EntityRegistryBackend>,
        scheduler: Arc<Scheduler>,
        env: Arc<Environment>,
    ) -> BoxFuture<Vec<Arc<StorageClient>>> {
        Box::new(
            scheduler
                .get_committees(cid)
                .and_then(move |committee: Vec<Committee>| -> BoxFuture<Node> {
                    let committee = committee
                        .iter()
                        .filter(|c| c.kind == CommitteeType::Storage)
                        .next();
                    if committee.is_none() {
                        return Box::new(future::err(Error::new("No storage committee")));
                    }
                    let storers = &committee.unwrap().members;

                    // TODO: rather than just looking up one committee member, registry should be queried for each.
                    let storer = &storers[0];
                    // Now look up the storer's ID in the registry.
                    registry.get_node(storer.public_key)
                })
                .and_then(move |node| -> BoxFuture<Vec<Arc<StorageClient>>> {
                    let env = env.clone();
                    Box::new(future::ok(vec![Arc::new(StorageClient::from_node(
                        node, env,
                    ))]))
                }),
        )
    }

    /// Get an active and connected StorageClient that storage requests can be routed to.
    fn get_storage(&self, max_retries: usize) -> BoxFuture<Arc<StorageClient>> {
        let shared_storage = self.clients.clone();
        let shared_inner = self.inner.clone();

        let attempts = future::loop_fn(
            max_retries,
            move |retries| -> BoxFuture<future::Loop<Arc<StorageClient>, usize>> {
                let known_storage = shared_storage.clone();
                let this_inner = shared_inner.clone();
                let nodes = known_storage.lock().unwrap();

                match nodes.first() {
                    None => {
                        let known_storage = shared_storage.clone();
                        return Box::new(
                            StorageFrontend::refresh(
                                this_inner.contract_id,
                                this_inner.registry.clone(),
                                this_inner.scheduler.clone(),
                                this_inner.environment.clone(),
                            ).then(move |response| match response {
                                Ok(mut clients) => {
                                    let mut nodes = known_storage.lock().unwrap();
                                    nodes.append(&mut clients);
                                    Ok(future::Loop::Continue(retries - 1))
                                }
                                Err(e) => Err(e),
                            }),
                        );
                    }
                    Some(client) => {
                        let out_client = client.clone();
                        Box::new(future::ok(future::Loop::Break(out_client)))
                    }
                }
            },
        );

        Box::new(attempts)
    }
}

impl StorageBackend for StorageFrontend {
    fn get(&self, key: H256) -> BoxFuture<Vec<u8>> {
        Box::new(
            self.get_storage(self.inner.retries)
                .and_then(move |s| s.get(key)),
        )
    }

    fn insert(&self, value: Vec<u8>, expiry: u64) -> BoxFuture<()> {
        Box::new(
            self.get_storage(self.inner.retries)
                .and_then(move |s| s.insert(value, expiry)),
        )
    }
}