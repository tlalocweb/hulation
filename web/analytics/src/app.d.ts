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
    /** hulaConfig is injected server-side by hula into the analytics
     * index.html as an inline <script>window.hulaConfig=...</script>
     * before hydration. The /analytics/config.json endpoint mirrors
     * the same payload for clients that prefer to fetch it. */
    hulaConfig?: {
      servers: Array<{ id: string; host: string; name?: string }>;
      /** ID of the server matched by the request host (or its
       * aliases). Empty when the request host doesn't correspond
       * to any configured server. */
      currentServerId?: string;
      username?: string;
      isAdmin?: boolean;
    };
  }
}

export {};
