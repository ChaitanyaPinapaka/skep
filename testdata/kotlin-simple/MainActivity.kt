package com.example.android.ui

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import com.example.android.data.AuthRepository

class MainActivity : ComponentActivity() {

    private val authRepository: AuthRepository by lazy {
        AuthRepository()
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContent {
            MainScreen(repository = authRepository)
        }
    }

    private fun doStuff(id: String): Boolean {
        return authRepository.fetch(id) != null
    }

    companion object {
        const val TAG = "MainActivity"

        fun newIntent(ctx: android.content.Context): android.content.Intent {
            return android.content.Intent(ctx, MainActivity::class.java)
        }
    }
}

interface AuthListener {
    fun onAuthSuccess(token: String)
    fun onAuthError(error: Throwable)
}

sealed class AuthState {
    object Idle : AuthState()
    data class Authenticated(val token: String) : AuthState()
    data class Failed(val error: String) : AuthState()
}

data class User(
    val id: String,
    val email: String,
    val displayName: String
)

typealias AuthCallback = (AuthState) -> Unit

enum class Role {
    ADMIN,
    USER,
    GUEST
}

@androidx.compose.runtime.Composable
fun MainScreen(repository: AuthRepository) {
    androidx.compose.material3.Text("Hello")
}

fun topLevelUtility(input: String): String = input.uppercase()

val SERVER_URL = "https://api.example.com"

private const val MAX_RETRIES = 3
