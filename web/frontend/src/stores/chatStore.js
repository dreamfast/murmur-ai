/**
 * Shared reactive state for the chat view.
 *
 * This module provides a singleton reactive store that is shared between
 * the Chat layout (sidebar, user list) and ChatContent (messages, input).
 * Using a plain reactive object avoids the need for Pinia or Vuex.
 */

import { reactive } from "vue";

export const chatStore = reactive({
  /** Sorted list of nicks in the current channel. */
  users: [],
  /** Current channel topic. */
  topic: "",
  /** Current channel name. */
  channel: "#murmur",
});
