package jp.shibu.hwmediacodec.player;

import android.app.Activity;
import android.os.Bundle;

import jp.shibu.hwmediacodec.mobile.EbitenView;

// The player as an Android application: one EbitenView, which runs the Go
// game bound into mobile.aar. The game opens the bundled clip through the
// hwmediacodec MediaCodec backend and shows which codec serves it. A plain
// Activity without AndroidX keeps the app buildable with the SDK tools
// alone (build-apk.sh) as well as with Gradle.
public class MainActivity extends Activity {
    private EbitenView view;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        setContentView(R.layout.activity_main);
        view = findViewById(R.id.ebitenview);
    }

    // Ebitengine asks that the game be suspended while the activity is
    // paused; the decoder keeps its MediaCodec and resumes where it was.
    @Override
    protected void onPause() {
        super.onPause();
        view.suspendGame();
    }

    @Override
    protected void onResume() {
        super.onResume();
        view.resumeGame();
    }
}
