// See https://kit.svelte.dev/docs/types#app
// for information about these interfaces
declare global {
  namespace App {
    // interface Error {}
    // interface Locals {}
    // interface PageData {}
    // interface PageState {}
    // interface Platform {}
  }

  interface Window {
    /** hulaConfig is injected server-side by hula at /analytics/config.json. */
    hulaConfig?: {
      servers: Array<{ id: string; host: string; name?: string }>;
      username?: string;
      isAdmin?: boolean;
    };
  }
}

export {};
