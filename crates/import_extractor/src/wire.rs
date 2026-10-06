use crate::{pb, rust};
use prost::Message;

pub fn dispatch(bytes: &[u8]) -> Vec<u8> {
    let result = std::panic::catch_unwind(|| -> Result<Vec<pb::CrateResult>, String> {
        let request = pb::Request::decode(bytes).map_err(|e| e.to_string())?;
        request
            .crates
            .iter()
            .map(|c| rust::extract(&c.root))
            .collect()
    });
    let response = match result {
        Ok(Ok(results)) => pb::Response {
            results,
            error: String::new(),
        },
        Ok(Err(error)) => pb::Response {
            results: vec![],
            error,
        },
        Err(_) => pb::Response {
            results: vec![],
            error: "extractor panicked".into(),
        },
    };
    response.encode_to_vec()
}
