// Immediate MSI action: changes session properties only, never machine state.
// Deferred policy actions receive this fresh identifier in their command lines.
#define WIN32_LEAN_AND_MEAN
#include <windows.h>
#include <msi.h>
#include <msiquery.h>
#include <bcrypt.h>

__declspec(dllexport) UINT __stdcall NewPolicyTransaction(MSIHANDLE session) {
    unsigned char random[16];
    wchar_t token[33];
    const wchar_t hex[] = L"0123456789abcdef";
    if (BCryptGenRandom(NULL, random, sizeof(random), BCRYPT_USE_SYSTEM_PREFERRED_RNG) != 0)
        return ERROR_INSTALL_FAILURE;
    for (unsigned int i = 0; i < sizeof(random); ++i) {
        token[2 * i] = hex[random[i] >> 4];
        token[2 * i + 1] = hex[random[i] & 15];
    }
    token[32] = 0;
    return MsiSetPropertyW(session, L"WinunitdPolicyTransaction", token);
}
