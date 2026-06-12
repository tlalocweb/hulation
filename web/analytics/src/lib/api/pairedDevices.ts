// Typed wrappers for the QR-paired device admin endpoints.
//
//   GET  /api/v1/pair/devices?all=true     — list every paired device (admin)
//   POST /api/v1/pair/devices/revoke       — revoke one device (owner or admin)
//
// Backed by server/pair_handlers.go. The list response uses snake_case fields
// (the handler hand-rolls the JSON, matching the gRPC-gateway proto-name style
// used elsewhere).

import { authHeaders, handle } from './http';

export interface PairedDevice {
  // The handler (writeDeviceList in server/pair_handlers.go) always emits all
  // five keys, so they're required strings here — though some (notably
  // server_id) may be the empty string, which the UI renders as a placeholder.
  device_id: string;
  user_id: string;
  server_id: string;
  public_key_b64: string;
  created_at: string;
}

export interface ListDevicesResponse {
  devices?: PairedDevice[];
}

export interface RevokeDeviceResponse {
  revoked?: boolean;
  device_id?: string;
}

export const pairedDevices = {
  // List every paired device across all users (admin-only on the server). The
  // SPA sorts for display; the API returns them unsorted.
  listAll: async (): Promise<PairedDevice[]> => {
    const res = await fetch('/api/v1/pair/devices?all=true', {
      headers: authHeaders(),
    });
    const data = await handle<ListDevicesResponse>(res);
    return data.devices ?? [];
  },

  // Revoke a single device by id. Idempotent server-side: revoking an unknown
  // device returns { revoked: false } rather than erroring.
  revoke: async (device_id: string): Promise<RevokeDeviceResponse> => {
    const res = await fetch('/api/v1/pair/devices/revoke', {
      method: 'POST',
      headers: { ...authHeaders(), 'Content-Type': 'application/json' },
      body: JSON.stringify({ device_id }),
    });
    return handle<RevokeDeviceResponse>(res);
  },
};
