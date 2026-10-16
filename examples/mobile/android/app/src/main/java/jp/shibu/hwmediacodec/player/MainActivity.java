package jp.shibu.hwmediacodec.player;

import android.app.Activity;
import android.content.ActivityNotFoundException;
import android.content.Intent;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.view.WindowManager;

import jp.shibu.hwmediacodec.mobile.EbitenView;
import jp.shibu.hwmediacodec.mobile.Host;
import jp.shibu.hwmediacodec.mobile.Mobile;

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

        // The check mode's Share and Open issue buttons, and the model for
        // its report, which Go cannot read.
        Mobile.setHost(new ReportHost());
        Mobile.setDeviceInfo(deviceInfo());
        // The check runs for a minute without a touch; the screen stays on
        // so that the codecs are not taken away in the middle of it.
        getWindow().addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON);
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

    // deviceInfo is the maker, model, SoC and Android version.
    private static String deviceInfo() {
        String soc = Build.SOC_MANUFACTURER + " " + Build.SOC_MODEL;
        return Build.MANUFACTURER + " " + Build.MODEL + " (" + soc + "), Android " + Build.VERSION.RELEASE
                + " (API " + Build.VERSION.SDK_INT + ")";
    }

    // ReportHost is the Go check's Host (check.go): the share sheet for the
    // Markdown report, which also offers Copy, and the browser for the
    // prefilled GitHub issue. Go calls it from its own thread.
    private final class ReportHost implements Host {
        @Override
        public void share(String subject, String text) {
            runOnUiThread(() -> {
                Intent send = new Intent(Intent.ACTION_SEND);
                send.setType("text/plain");
                send.putExtra(Intent.EXTRA_SUBJECT, subject);
                send.putExtra(Intent.EXTRA_TEXT, text);
                startActivity(Intent.createChooser(send, subject));
            });
        }

        @Override
        public void openURL(String url) {
            runOnUiThread(() -> {
                try {
                    startActivity(new Intent(Intent.ACTION_VIEW, Uri.parse(url)));
                } catch (ActivityNotFoundException e) {
                    // No browser: Share still carries the report.
                }
            });
        }
    }
}
