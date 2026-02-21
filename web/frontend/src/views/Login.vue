<template>
  <div class="flex min-h-screen items-center justify-center bg-bg-primary px-4">
    <div class="w-full max-w-sm space-y-6">
      <!-- Logo / Title -->
      <div class="text-center">
        <h1 class="font-mono text-3xl font-bold text-accent">murmur</h1>
        <p class="mt-1 text-sm text-text-secondary">IRC Dashboard</p>
      </div>

      <!-- Login Card -->
      <form
        class="space-y-4 rounded-lg border border-border bg-bg-secondary p-6"
        @submit.prevent="handleLogin"
      >
        <!-- Error message -->
        <div
          v-if="error"
          class="rounded border border-error/30 bg-error/10 px-3 py-2 text-sm text-error"
        >
          {{ error }}
        </div>

        <!-- Nick field -->
        <div class="space-y-1">
          <label for="nick" class="block text-sm font-medium text-text-secondary">
            Nick
          </label>
          <input
            id="nick"
            v-model="nick"
            type="text"
            autocomplete="username"
            required
            :disabled="loading"
            class="w-full rounded border border-border bg-bg-input px-3 py-2 font-mono text-sm text-text-primary placeholder-text-muted outline-none transition focus:border-border-focus focus:ring-1 focus:ring-accent/50 disabled:opacity-50"
            placeholder="your_nick"
          />
        </div>

        <!-- Password field -->
        <div class="space-y-1">
          <label for="password" class="block text-sm font-medium text-text-secondary">
            Password
          </label>
          <input
            id="password"
            v-model="password"
            type="password"
            autocomplete="current-password"
            required
            :disabled="loading"
            class="w-full rounded border border-border bg-bg-input px-3 py-2 font-mono text-sm text-text-primary placeholder-text-muted outline-none transition focus:border-border-focus focus:ring-1 focus:ring-accent/50 disabled:opacity-50"
            placeholder="NickServ password"
          />
        </div>

        <!-- Submit button -->
        <button
          type="submit"
          :disabled="loading || !nick || !password"
          class="w-full rounded bg-accent py-2 text-sm font-medium text-white transition hover:bg-accent-hover disabled:cursor-not-allowed disabled:opacity-50"
        >
          <span v-if="loading">Authenticating...</span>
          <span v-else>Log in</span>
        </button>
      </form>

      <!-- Footer -->
      <p class="text-center text-xs text-text-muted">
        Authenticate with your IRC nick and NickServ password.
      </p>
    </div>
  </div>
</template>

<script setup>
import { ref } from "vue";
import { useRouter } from "vue-router";
import { SESSION_NICK_KEY, SESSION_SIGNING_KEY, API } from "../constants.js";

const router = useRouter();

const nick = ref("");
const password = ref("");
const loading = ref(false);
const error = ref("");

async function handleLogin() {
  loading.value = true;
  error.value = "";

  try {
    const res = await fetch(API.LOGIN, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ nick: nick.value, password: password.value }),
    });

    let data;
    try {
      data = await res.json();
    } catch {
      error.value = "Unexpected server response";
      return;
    }

    if (!res.ok || !data.ok) {
      error.value = data.error || "Login failed";
      return;
    }

    // Store nick and signing key in sessionStorage.
    sessionStorage.setItem(SESSION_NICK_KEY, data.nick);
    if (data.signing_key) {
      sessionStorage.setItem(SESSION_SIGNING_KEY, data.signing_key);
    }

    router.push({ name: "overview" });
  } catch {
    error.value = "Network error — is the server running?";
  } finally {
    loading.value = false;
  }
}
</script>
