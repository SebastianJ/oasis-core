//! Runtime transaction batch dispatcher.
use std::{collections::HashMap, sync::Arc};

use failure::{Fallible, ResultExt};
use io_context::Context as IoContext;
use serde::{de::DeserializeOwned, Serialize};

use super::{
    context::Context,
    tags::Tags,
    types::{TxnBatch, TxnCall, TxnOutput},
};
use crate::{
    common::{cbor, crypto::hash::Hash},
    storage::{context::StorageContext, mkvs::MKVS, KeyValue},
};

/// Dispatch error.
#[derive(Debug, Fail)]
enum DispatchError {
    #[fail(display = "method not found: {}", method)]
    MethodNotFound { method: String },
}

/// Storage context configuration to be used during transaction dispatch.
pub struct DispatchStorageContext<'a> {
    /// Reference to the MKVS backend.
    pub mkvs: &'a mut dyn MKVS,
    /// Reference to the untrusted local key/value store.
    pub untrusted_local: &'a Arc<dyn KeyValue>,
}

/// Error indicating that performing a transaction check was successful.
#[derive(Debug, Fail)]
#[fail(display = "transaction check successful")]
pub struct CheckOnlySuccess;

/// Custom batch handler.
///
/// A custom batch handler can be configured on the `Dispatcher` and will have
/// its `start_batch` and `end_batch` methods called at the appropriate times.
pub trait BatchHandler {
    /// Called before the first call in a batch is dispatched.
    ///
    /// The context may be mutated and will be available as read-only to all
    /// runtime calls.
    fn start_batch(&self, ctx: &mut Context);

    /// Called after all calls have been dispatched.
    fn end_batch(&self, ctx: &mut Context);
}

/// Custom context initializer.
pub trait ContextInitializer {
    /// Called to initialize the context.
    fn init(&self, ctx: &mut Context);
}

impl<F> ContextInitializer for F
where
    F: Fn(&mut Context),
{
    fn init(&self, ctx: &mut Context) {
        (*self)(ctx)
    }
}

/// Custom finalizer.
pub trait Finalizer {
    /// Called to finalize transaction.
    ///
    /// This method is called after storage has been finalized so the
    /// storage context is not available and using it will panic.
    fn finalize(&self, new_storage_root: Hash);
}

impl<F> Finalizer for F
where
    F: Fn(Hash),
{
    fn finalize(&self, new_storage_root: Hash) {
        (*self)(new_storage_root)
    }
}

/// Descriptor of a runtime API method.
#[derive(Clone, Debug)]
pub struct MethodDescriptor {
    /// Method name.
    pub name: String,
}

/// Handler for a runtime method.
pub trait MethodHandler<Call, Output> {
    /// Invoke the method implementation and return a response.
    fn handle(&self, call: &Call, ctx: &mut Context) -> Fallible<Output>;
}

impl<Call, Output, F> MethodHandler<Call, Output> for F
where
    Call: 'static,
    Output: 'static,
    F: Fn(&Call, &mut Context) -> Fallible<Output> + 'static,
{
    fn handle(&self, call: &Call, ctx: &mut Context) -> Fallible<Output> {
        (*self)(&call, ctx)
    }
}

/// Dispatcher for a runtime method.
pub trait MethodHandlerDispatch {
    /// Get method descriptor.
    fn get_descriptor(&self) -> &MethodDescriptor;

    /// Dispatches the given raw call.
    fn dispatch(
        &self,
        storage: &mut DispatchStorageContext,
        call: TxnCall,
        ctx: &mut Context,
    ) -> Fallible<cbor::Value>;
}

struct MethodHandlerDispatchImpl<Call, Output> {
    /// Method descriptor.
    descriptor: MethodDescriptor,
    /// Method handler.
    handler: Box<dyn MethodHandler<Call, Output>>,
}

impl<Call, Output> MethodHandlerDispatch for MethodHandlerDispatchImpl<Call, Output>
where
    Call: DeserializeOwned + 'static,
    Output: Serialize + 'static,
{
    fn get_descriptor(&self) -> &MethodDescriptor {
        &self.descriptor
    }

    fn dispatch(
        &self,
        storage: &mut DispatchStorageContext,
        call: TxnCall,
        ctx: &mut Context,
    ) -> Fallible<cbor::Value> {
        let predicted_rw_set = call.predicted_rw_set;
        let call = cbor::from_value(call.args).context("unable to parse call arguments")?;

        let response = if predicted_rw_set.is_empty() {
            // If the predicted read/write set is empty, there is no need to do any
            // verification. It is very important that this notion of an empty read
            // write set is understood by the transaction scheduler in the same way.
            StorageContext::enter(storage.mkvs, storage.untrusted_local.clone(), || {
                self.handler.handle(&call, ctx)
            })
        } else {
            // Wrap current storage context into a read-write set verifier.
            let mut verifier = predicted_rw_set.into_verifier(storage.mkvs);
            let response =
                StorageContext::enter(&mut verifier, storage.untrusted_local.clone(), || {
                    self.handler.handle(&call, ctx)
                });

            // Try to commit.
            verifier.commit(IoContext::create_child(&ctx.io_ctx))?;
            response
        };

        Ok(cbor::to_value(response?))
    }
}

/// Runtime method dispatcher implementation.
pub struct Method {
    /// Method dispatcher.
    dispatcher: Box<dyn MethodHandlerDispatch>,
}

impl Method {
    /// Create a new enclave method descriptor.
    pub fn new<Call, Output, Handler>(method: MethodDescriptor, handler: Handler) -> Self
    where
        Call: DeserializeOwned + 'static,
        Output: Serialize + 'static,
        Handler: MethodHandler<Call, Output> + 'static,
    {
        Method {
            dispatcher: Box::new(MethodHandlerDispatchImpl {
                descriptor: method,
                handler: Box::new(handler),
            }),
        }
    }

    /// Return method name.
    pub fn get_name(&self) -> &String {
        &self.dispatcher.get_descriptor().name
    }

    /// Dispatch method call.
    pub fn dispatch(
        &self,
        storage: &mut DispatchStorageContext,
        call: TxnCall,
        ctx: &mut Context,
    ) -> Fallible<cbor::Value> {
        self.dispatcher.dispatch(storage, call, ctx)
    }
}

/// Runtime method dispatcher.
///
/// The dispatcher holds all registered runtime methods and provides an entry point
/// for their invocation.
pub struct Dispatcher {
    /// Registered runtime methods.
    methods: HashMap<String, Method>,
    /// Registered batch handler.
    batch_handler: Option<Box<dyn BatchHandler>>,
    /// Registered context initializer.
    ctx_initializer: Option<Box<dyn ContextInitializer>>,
    /// Registered finalizer.
    finalizer: Option<Box<dyn Finalizer>>,
}

impl Dispatcher {
    /// Create a new runtime method dispatcher instance.
    pub fn new() -> Self {
        Dispatcher {
            methods: HashMap::new(),
            batch_handler: None,
            ctx_initializer: None,
            finalizer: None,
        }
    }

    /// Register a new method in the dispatcher.
    pub fn add_method(&mut self, method: Method) {
        self.methods.insert(method.get_name().clone(), method);
    }

    /// Configure batch handler.
    pub fn set_batch_handler<H>(&mut self, handler: H)
    where
        H: BatchHandler + 'static,
    {
        self.batch_handler = Some(Box::new(handler));
    }

    /// Configure context initializer.
    pub fn set_context_initializer<I>(&mut self, initializer: I)
    where
        I: ContextInitializer + 'static,
    {
        self.ctx_initializer = Some(Box::new(initializer));
    }

    /// Configure finalizer.
    pub fn set_finalizer<F>(&mut self, finalizer: F)
    where
        F: Finalizer + 'static,
    {
        self.finalizer = Some(Box::new(finalizer));
    }

    /// Dispatches a batch of runtime requests.
    pub fn dispatch_batch(
        &self,
        mut storage: DispatchStorageContext,
        batch: &TxnBatch,
        mut ctx: Context,
    ) -> (TxnBatch, Vec<Tags>) {
        if let Some(ref ctx_init) = self.ctx_initializer {
            ctx_init.init(&mut ctx);
        }

        // Invoke start batch handler.
        if let Some(ref handler) = self.batch_handler {
            handler.start_batch(&mut ctx);
        }

        // Process batch.
        let outputs = TxnBatch(
            batch
                .iter()
                .map(|call| {
                    ctx.start_transaction();
                    self.dispatch(&mut storage, call, &mut ctx)
                })
                .collect(),
        );

        // Invoke end batch handler.
        if let Some(ref handler) = self.batch_handler {
            handler.end_batch(&mut ctx);
        }

        (outputs, ctx.close())
    }

    /// Dispatches a raw runtime invocation request.
    pub fn dispatch(
        &self,
        storage: &mut DispatchStorageContext,
        call: &Vec<u8>,
        ctx: &mut Context,
    ) -> Vec<u8> {
        let rsp = match self.dispatch_fallible(storage, call, ctx) {
            Ok(response) => TxnOutput::Success(response),
            Err(error) => {
                if let Some(check_msg) = error.downcast_ref::<CheckOnlySuccess>() {
                    TxnOutput::Success(cbor::Value::Text(format!("{}", check_msg)))
                } else {
                    TxnOutput::Error(format!("{}", error))
                }
            }
        };

        cbor::to_vec(&rsp)
    }

    fn dispatch_fallible(
        &self,
        storage: &mut DispatchStorageContext,
        call: &Vec<u8>,
        ctx: &mut Context,
    ) -> Fallible<cbor::Value> {
        let call: TxnCall = cbor::from_slice(call).context("unable to parse call")?;

        match self.methods.get(&call.method) {
            Some(dispatcher) => dispatcher.dispatch(storage, call, ctx),
            None => Err(DispatchError::MethodNotFound {
                method: call.method,
            }
            .into()),
        }
    }

    /// Invoke the finalizer (if any).
    pub fn finalize(&self, new_storage_root: Hash) {
        if let Some(ref finalizer) = self.finalizer {
            finalizer.finalize(new_storage_root);
        }
    }
}

#[cfg(test)]
mod tests {
    use std::sync::Arc;

    use failure::Fallible;
    use io_context::Context as IoContext;
    use serde_derive::{Deserialize, Serialize};

    use crate::{
        common::{
            cbor,
            crypto::hash::Hash,
            roothash::{Header, Namespace},
        },
        storage::{
            mkvs::{Prefix, WriteLog, MKVS},
            KeyValue,
        },
    };

    use super::*;

    const TEST_TIMESTAMP: u64 = 0xcafedeadbeefc0de;

    #[derive(Debug, Eq, PartialEq, Serialize, Deserialize)]
    struct Complex {
        text: String,
        number: u32,
    }

    /// Register a dummy method.
    fn register_dummy_method(dispatcher: &mut Dispatcher) {
        // Register dummy runtime method.
        dispatcher.add_method(Method::new(
            MethodDescriptor {
                name: "dummy".to_owned(),
            },
            |call: &Complex, ctx: &mut Context| -> Fallible<Complex> {
                assert_eq!(ctx.header.timestamp, TEST_TIMESTAMP);

                Ok(Complex {
                    text: call.text.clone(),
                    number: call.number * 2,
                })
            },
        ));
    }

    struct Dummy;

    impl MKVS for Dummy {
        fn get(&mut self, _ctx: IoContext, _key: &[u8]) -> Option<Vec<u8>> {
            unimplemented!();
        }

        fn insert(&mut self, _ctx: IoContext, _key: &[u8], _value: &[u8]) -> Option<Vec<u8>> {
            unimplemented!();
        }

        fn remove(&mut self, _ctx: IoContext, _key: &[u8]) -> Option<Vec<u8>> {
            unimplemented!();
        }

        fn prefetch_prefixes(&self, _ctx: IoContext, _prefixes: &Vec<Prefix>, _limit: u16) {
            unimplemented!();
        }

        fn commit(
            &mut self,
            _ctx: IoContext,
            _namespace: Namespace,
            _round: u64,
        ) -> Fallible<(WriteLog, Hash)> {
            unimplemented!();
        }

        fn rollback(&mut self) {
            unimplemented!();
        }
    }

    impl KeyValue for Dummy {
        fn get(&self, _key: Vec<u8>) -> Fallible<Vec<u8>> {
            unimplemented!();
        }

        fn insert(&self, _key: Vec<u8>, _value: Vec<u8>) -> Fallible<()> {
            unimplemented!();
        }
    }

    #[test]
    fn test_dispatcher() {
        let mut dispatcher = Dispatcher::new();
        register_dummy_method(&mut dispatcher);

        // Prepare a dummy call.
        let call = TxnCall {
            method: "dummy".to_owned(),
            args: cbor::to_value(Complex {
                text: "hello".to_owned(),
                number: 21,
            }),
            predicted_rw_set: Default::default(),
        };
        let call_encoded = cbor::to_vec(&call);

        let header = Header {
            timestamp: TEST_TIMESTAMP,
            ..Default::default()
        };
        let mut ctx = Context::new(IoContext::background().freeze(), &header, false);

        // Call runtime.
        let mut dummy_mkvs = Dummy;
        let dummy_kv: Arc<dyn KeyValue> = Arc::new(Dummy);
        let result = dispatcher.dispatch(
            &mut DispatchStorageContext {
                mkvs: &mut dummy_mkvs,
                untrusted_local: &dummy_kv,
            },
            &call_encoded,
            &mut ctx,
        );

        // Decode result.
        let result_decoded: TxnOutput = cbor::from_slice(&result).unwrap();
        match result_decoded {
            TxnOutput::Success(value) => {
                let value: Complex = cbor::from_value(value).unwrap();

                assert_eq!(
                    value,
                    Complex {
                        text: "hello".to_owned(),
                        number: 42
                    }
                );
            }
            _ => panic!("txn call should return success"),
        }
    }
}
