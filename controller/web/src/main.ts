import { createApp } from 'vue'
import { i18n } from './i18n'
import { router } from './app/router'
import App from './App.vue'
import './style.css'
const app = createApp(App).use(i18n).use(router)
void router.isReady().then(() => app.mount('#app'))
