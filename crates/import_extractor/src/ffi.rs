use std::slice;

/// # Safety
/// Input points to `len` readable bytes (or is null when len is zero).
/// Both output pointers are writable. Free the returned allocation exactly once.
#[no_mangle]
pub unsafe extern "C" fn gazelle_rs_ie_dispatch(
    input: *const u8,
    len: usize,
    output: *mut *mut u8,
    output_len: *mut usize,
) {
    let bytes = if len == 0 {
        &[]
    } else {
        unsafe { slice::from_raw_parts(input, len) }
    };
    let response = crate::wire::dispatch(bytes).into_boxed_slice();
    unsafe {
        *output_len = response.len();
        *output = Box::into_raw(response) as *mut u8;
    }
}

/// # Safety
/// The pointer and length must be an allocation returned by dispatch, not yet freed.
#[no_mangle]
pub unsafe extern "C" fn gazelle_rs_ie_free(ptr: *mut u8, len: usize) {
    if !ptr.is_null() {
        unsafe { drop(Box::from_raw(slice::from_raw_parts_mut(ptr, len))) };
    }
}

#[cfg(test)]
mod tests {
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
}
