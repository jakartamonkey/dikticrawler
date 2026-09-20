package crawler

import (
	"fmt"
	"strconv"
	"strings"

	"sekolah-crawler/internal/api"
)

// ruangCategories/ruangSuffixes enumerate the facilities ("ruang" = rooms,
// i.e. classrooms, library, labs) block. Verified stable across TK, SD, SMP,
// SMA, SMK and PKBM sample profiles: same 10 categories x 4 condition states.
var ruangCategories = []string{
	"ruang_kelas", "ruang_perpustakaan",
	"laboratorium_bahasa", "laboratorium_ipa", "laboratorium_ips",
	"laboratorium_komputer", "laboratorium_multimedia",
	"laboratorium_fisika", "laboratorium_kimia", "laboratorium_biologi",
}
var ruangSuffixes = []string{"baik", "rusak_ringan", "rusak_sedang", "rusak_berat"}

func RuangFields() []string {
	fields := make([]string, 0, len(ruangCategories)*len(ruangSuffixes))
	for _, c := range ruangCategories {
		for _, s := range ruangSuffixes {
			fields = append(fields, c+"_"+s)
		}
	}
	return fields
}

// DetailCSVHeader is the fixed column order for the detail-phase CSV output.
func DetailCSVHeader() []string {
	h := []string{
		"sekolah_id", "npsn", "nama", "bentuk_pendidikan", "status_sekolah", "akreditasi",
		"provinsi", "kabupaten", "kecamatan", "nama_dusun", "alamat_jalan", "rt", "rw", "kode_pos",
		"lintang", "bujur", "nomor_telepon", "email", "website",
		"yayasan_id", "yayasan",
		"waktu_penyelenggaraan", "semester_id", "semester_keterangan",
		"luas_tanah_milik", "luas_tanah_bukan_milik",
		"daya_listrik", "sumber_listrik", "akses_internet", "akses_internet_2",
	}
	h = append(h, RuangFields()...) // "utilitas" / facilities block
	h = append(h,
		"kurikulum_terbaru", "kurikulum_semester_terbaru", "kurikulum_all",
		"ptk_guru_l", "ptk_guru_p",
		"jml_pd", "jml_pd_l", "jml_pd_p", "jml_rombel", "rasio_siswa_rombel",
		"rasio_rombel_ruang_kelas", "rasio_siswa_guru",
		"persentase_guru_klasifikasi", "persentase_guru_sertifikasi", "persentase_guru_asn",
		"persentase_ruang_kelas_layak",
	)
	return h
}

func strv(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
func fltv(p *float64) string {
	if p == nil {
		return ""
	}
	return strconv.FormatFloat(*p, 'f', -1, 64)
}
func intv(p *int) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(*p)
}

// FlattenDetail maps one full-detail response onto DetailCSVHeader's column order.
func FlattenDetail(sekolahID string, d *api.FullDetailData) []string {
	row := make(map[string]string, 64)
	row["sekolah_id"] = sekolahID

	if len(d.Sekolah) > 0 {
		sk := d.Sekolah[0]
		row["npsn"] = sk.Npsn
		row["nama"] = sk.Nama
		row["bentuk_pendidikan"] = sk.BentukPendidikan
		row["status_sekolah"] = sk.StatusSekolah
		row["akreditasi"] = strv(sk.Akreditasi)
		row["provinsi"] = sk.Provinsi
		row["kabupaten"] = sk.Kabupaten
		row["kecamatan"] = sk.Kecamatan
		row["nama_dusun"] = strv(sk.NamaDusun)
		row["alamat_jalan"] = sk.AlamatJalan
		row["rt"] = intv(sk.Rt)
		row["rw"] = intv(sk.Rw)
		row["kode_pos"] = strv(sk.KodePos)
		row["lintang"] = fltv(sk.Lintang)
		row["bujur"] = fltv(sk.Bujur)
		row["nomor_telepon"] = strv(sk.NomorTelepon)
		row["email"] = strv(sk.Email)
		row["website"] = strv(sk.Website)
		row["yayasan_id"] = strv(sk.YayasanID)
		row["yayasan"] = strv(sk.Yayasan)
		row["waktu_penyelenggaraan"] = strv(sk.WaktuPenyelenggaraan)
		row["semester_id"] = strv(sk.SemesterID)
		row["semester_keterangan"] = strv(sk.SemesterKeterangan)
		row["luas_tanah_milik"] = fltv(sk.LuasTanahMilik)
		row["luas_tanah_bukan_milik"] = fltv(sk.LuasTanahBukanMilik)
		row["daya_listrik"] = fltv(sk.DayaListrik)
		row["sumber_listrik"] = strv(sk.SumberListrik)
		row["akses_internet"] = strv(sk.AksesInternet)
		row["akses_internet_2"] = strv(sk.AksesInternet2)
	}

	for _, field := range RuangFields() {
		row[field] = ""
	}
	if len(d.Ruang) > 0 {
		for k, v := range d.Ruang[0] {
			if k == "sekolah_id" {
				continue
			}
			row[k] = fmt.Sprintf("%v", v)
		}
	}

	if len(d.Kurikulum) > 0 {
		row["kurikulum_terbaru"] = d.Kurikulum[0].Kurikulum
		row["kurikulum_semester_terbaru"] = d.Kurikulum[0].SemesterID
		parts := make([]string, 0, len(d.Kurikulum))
		for _, k := range d.Kurikulum {
			parts = append(parts, k.SemesterID+":"+k.Kurikulum)
		}
		row["kurikulum_all"] = strings.Join(parts, "; ")
	}

	if len(d.Ptk) > 0 {
		row["ptk_guru_l"] = intv(d.Ptk[0].PtkGuruL)
		row["ptk_guru_p"] = intv(d.Ptk[0].PtkGuruP)
	}
	if len(d.RasioSiswa) > 0 {
		rs := d.RasioSiswa[0]
		row["jml_pd"] = intv(rs.JmlPd)
		row["jml_pd_l"] = intv(rs.JmlPdL)
		row["jml_pd_p"] = intv(rs.JmlPdP)
		row["jml_rombel"] = intv(rs.JmlRombel)
		row["rasio_siswa_rombel"] = fltv(rs.RasioSiswaRombel)
	}
	if len(d.RasioRombelRuangKelas) > 0 {
		row["rasio_rombel_ruang_kelas"] = fltv(d.RasioRombelRuangKelas[0].RasioRombelRuangKelas)
	}
	if len(d.RasioSiswaGuru) > 0 {
		row["rasio_siswa_guru"] = fltv(d.RasioSiswaGuru[0].RasioSiswaGuru)
	}
	if len(d.PersentaseGuru) > 0 {
		pg := d.PersentaseGuru[0]
		row["persentase_guru_klasifikasi"] = fltv(pg.PersentaseGuruKlasifikasi)
		row["persentase_guru_sertifikasi"] = fltv(pg.PersentaseGuruSertifikasi)
		row["persentase_guru_asn"] = fltv(pg.PersentaseGuruASN)
	}
	if len(d.PersentaseRuangKelasLayak) > 0 {
		row["persentase_ruang_kelas_layak"] = fltv(d.PersentaseRuangKelasLayak[0].Rasio)
	}

	header := DetailCSVHeader()
	out := make([]string, len(header))
	for idx, col := range header {
		out[idx] = row[col]
	}
	return out
}
