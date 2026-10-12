package jp.shibu.hwmediacodec.player

import android.os.Bundle
import androidx.appcompat.app.AppCompatActivity
import jp.shibu.hwmediacodec.mobile.EbitenView

// The player as an Android application: one EbitenView, which runs the Go
// game bound into mobile.aar. The game opens the bundled clip through the
// hwmediacodec MediaCodec backend and shows which codec serves it.
class MainActivity : AppCompatActivity() {
    private lateinit var view: EbitenView

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)
        view = findViewById(R.id.ebitenview)
    }

    // Ebitengine asks that the game be suspended while the activity is
    // paused; the decoder keeps its MediaCodec and resumes where it was.
    override fun onPause() {
        super.onPause()
        view.suspendGame()
    }

    override fun onResume() {
        super.onResume()
        view.resumeGame()
    }
}
