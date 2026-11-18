use super::*;
use prost::Message;
#[test]
fn malformed_request_returns_owned_error_response() {
    let input = [0xff];
    let mut output = std::ptr::null_mut();
    let mut len = 0;
    unsafe {
        gazelle_rs_ie_dispatch(input.as_ptr(), input.len(), &mut output, &mut len);
        let response = crate::pb::Response::decode(slice::from_raw_parts(output, len)).unwrap();
        assert!(!response.error.is_empty());
        gazelle_rs_ie_free(output, len);
        gazelle_rs_ie_free(std::ptr::null_mut(), 0);
    }
}
