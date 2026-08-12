//go:build darwin && cgo

package keychain

/*
#cgo darwin LDFLAGS: -framework Security -framework CoreFoundation
#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>
#include <stdlib.h>

static int keychain_get(const char *service, const char *account, void **value, size_t *value_len) {
	CFStringRef service_ref = CFStringCreateWithCString(NULL, service, kCFStringEncodingUTF8);
	CFStringRef account_ref = CFStringCreateWithCString(NULL, account, kCFStringEncodingUTF8);
	const void *keys[] = { kSecClass, kSecAttrService, kSecAttrAccount, kSecReturnData, kSecMatchLimit };
	const void *values[] = { kSecClassGenericPassword, service_ref, account_ref, kCFBooleanTrue, kSecMatchLimitOne };
	CFDictionaryRef query = CFDictionaryCreate(NULL, keys, values, 5, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	CFDataRef data = NULL;
	OSStatus status = SecItemCopyMatching(query, (CFTypeRef *)&data);
	CFRelease(query);
	CFRelease(account_ref);
	CFRelease(service_ref);
	if (status != errSecSuccess) return (int)status;
	*value_len = CFDataGetLength(data);
	*value = malloc(*value_len);
	if (*value == NULL && *value_len != 0) { CFRelease(data); return -1; }
	if (*value_len != 0) CFDataGetBytes(data, CFRangeMake(0, *value_len), *value);
	CFRelease(data);
	return 0;
}

static int keychain_set(const char *service, const char *account, const void *value, size_t value_len) {
	CFStringRef service_ref = CFStringCreateWithCString(NULL, service, kCFStringEncodingUTF8);
	CFStringRef account_ref = CFStringCreateWithCString(NULL, account, kCFStringEncodingUTF8);
	CFDataRef data_ref = CFDataCreate(NULL, value, value_len);
	const void *query_keys[] = { kSecClass, kSecAttrService, kSecAttrAccount };
	const void *query_values[] = { kSecClassGenericPassword, service_ref, account_ref };
	CFDictionaryRef query = CFDictionaryCreate(NULL, query_keys, query_values, 3, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	const void *data_keys[] = { kSecValueData };
	const void *data_values[] = { data_ref };
	CFDictionaryRef attributes = CFDictionaryCreate(NULL, data_keys, data_values, 1, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	OSStatus status = SecItemUpdate(query, attributes);
	if (status == errSecItemNotFound) {
		const void *add_keys[] = { kSecClass, kSecAttrService, kSecAttrAccount, kSecValueData };
		const void *add_values[] = { kSecClassGenericPassword, service_ref, account_ref, data_ref };
		CFDictionaryRef item = CFDictionaryCreate(NULL, add_keys, add_values, 4, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
		status = SecItemAdd(item, NULL);
		CFRelease(item);
	}
	CFRelease(attributes);
	CFRelease(query);
	CFRelease(data_ref);
	CFRelease(account_ref);
	CFRelease(service_ref);
	return (int)status;
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

func platformGetTokenImpl(service, account string) (string, error) {
	cService := C.CString(service)
	defer C.free(unsafe.Pointer(cService))
	cAccount := C.CString(account)
	defer C.free(unsafe.Pointer(cAccount))
	var value unsafe.Pointer
	var valueLen C.size_t
	status := C.keychain_get(cService, cAccount, &value, &valueLen)
	if status != 0 {
		return "", fmt.Errorf("security.framework status %d", int(status))
	}
	defer C.free(value)
	return string(C.GoBytes(value, C.int(valueLen))), nil
}

func platformSetTokenImpl(service, account, token string) error {
	cService := C.CString(service)
	defer C.free(unsafe.Pointer(cService))
	cAccount := C.CString(account)
	defer C.free(unsafe.Pointer(cAccount))
	value := C.CBytes([]byte(token))
	defer C.free(value)
	status := C.keychain_set(cService, cAccount, value, C.size_t(len(token)))
	if status != 0 {
		return fmt.Errorf("security.framework status %d", int(status))
	}
	return nil
}
