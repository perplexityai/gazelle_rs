pub mod ffi;
pub mod rust;
pub mod wire;

#[cfg(feature = "cargo")]
pub mod pb {
    include!(concat!(env!("OUT_DIR"), "/gazelle_rs.import_extractor.rs"));
}
#[cfg(not(feature = "cargo"))]
pub use import_extractor_proto::gazelle_rs::import_extractor as pb;
