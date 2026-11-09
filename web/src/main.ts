import { createApp } from 'vue'
import { i18n } from './i18n'
import { router } from './app/router'
import App from './App.vue'
import './style.css'
createApp(App).use(i18n).use(router).mount('#app')
